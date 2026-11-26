import { PlusOutlined } from '@ant-design/icons'
import { Button, Drawer, Form, Input, InputNumber, Popconfirm, Progress, Select, Space, Switch, Tag, Tooltip, Typography, type TableProps } from 'antd'
import { useCallback, useState } from 'react'
import { api, csrfHeader, type Agent, type AgentAllocation, type AgentCreateInput, type AgentUpdateInput, type Plan } from '../../api/client'
import { GB_PRECISION } from '../../bytes'
import { useSession } from '../../auth/session-context'
import { ErrorAlert } from '../../components/ErrorAlert'
import { FormModal } from '../../components/FormModal'
import { PagedTable } from '../../components/PagedTable'
import { PageHeader } from '../../components/PageHeader'
import { ResetCredentialModal, type ResetCredentialSave } from '../../components/ResetCredentialModal'
import { useDateRangeFilter } from '../../hooks/use-date-range-filter'
import { useRemote } from '../../hooks/use-remote'
import { useRowAction, useSubmit } from '../../hooks/use-submit'
import { PERMISSIONS } from '../../menu'
import { allocationColumns, describeAgentPlan, isWhitelistable } from '../agent/agent-format'
import { MAX_KEYWORD_LEN, MAX_REMARK_LEN } from '../form-limits'
import { bytesToGb, formatBytes, gbToBytes } from '../users/quota'
import { adminCredentialRules, adminUsernameRules } from './admin-form-rules'
import { formatTime } from './format'

// 超管侧的代理管理（docs/03 §5.23 超管侧管理，Ruling-103、Ruling-104）：读 admin:read，写 admin:write（写操作须二次验证，由 api 客户端统一弹框）

const MAX_AGENT_USERS = 1000000
const MAX_WHITELIST_PLANS = 200
const PERCENT_FULL = 100

/** 重置代理的登录密码：沿用管理员的重置密码接口（docs/03 §5.23）。 */
const saveAgentCredential: ResetCredentialSave<Agent> = (agent, credential) =>
  api.PUT('/api/admin/admins/{id}/password', { params: { ...csrfHeader(), path: { id: agent.id } }, body: { password: credential } })

/** 套餐列表请求与组件状态无关，提到模块级保持引用稳定。 */
const loadPlans = (signal: AbortSignal) => api.GET('/api/admin/plans', { signal })

function PoolCell({ agent }: { agent: Agent }) {
  const percent = agent.trafficPoolTotal === 0 ? PERCENT_FULL : Math.min(PERCENT_FULL, Math.round((agent.trafficPoolUsed / agent.trafficPoolTotal) * PERCENT_FULL))
  const text = `${formatBytes(agent.trafficPoolUsed)} / ${formatBytes(agent.trafficPoolTotal)}`
  return (
    <Tooltip title={text}>
      <Space orientation="vertical" size={0}>
        <span>{text}</span>
        <Progress percent={percent} size="small" showInfo={false} status={percent >= PERCENT_FULL ? 'exception' : 'normal'} className="usage-bar" />
      </Space>
    </Tooltip>
  )
}

/** 代理管理页：列表、新建、改配额与白名单、停用、开通记录、重置密码与清除两步验证（后两项沿用管理员接口）。 */
export function AgentsPage() {
  const { can } = useSession()
  const canWrite = can(PERMISSIONS.ADMIN_WRITE)
  const canReadPlans = can(PERMISSIONS.PLAN_READ)
  const [keyword, setKeyword] = useState('')
  const [refreshKey, setRefreshKey] = useState(0)
  const [editing, setEditing] = useState<Agent | null>(null)
  const [isFormOpen, setIsFormOpen] = useState(false)
  const [resetting, setResetting] = useState<Agent | null>(null)
  const [viewing, setViewing] = useState<Agent | null>(null)
  const reload = () => setRefreshKey((k) => k + 1)
  const loadAgents = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) =>
      api.GET('/api/admin/agents', { params: { query: { page, pageSize, keyword: keyword === '' ? undefined : keyword } }, signal }),
    [keyword],
  )
  const plans = useRemote<Plan[]>(loadPlans, canWrite && canReadPlans)

  const openForm = (agent: Agent | null) => {
    setEditing(agent)
    setIsFormOpen(true)
  }

  const act = useRowAction(reload)
  const clearTotp = (agent: Agent) => act(() => api.DELETE('/api/admin/admins/{id}/totp', { params: { ...csrfHeader(), path: { id: agent.id } } }), '已清除两步验证')

  const columns: TableProps<Agent>['columns'] = [
    {
      title: '用户名',
      dataIndex: 'username',
      render: (name: string, a: Agent) => (
        <Space orientation="vertical" size={0}>
          {name}
          {a.remark !== '' && <Typography.Text type="secondary">{a.remark}</Typography.Text>}
        </Space>
      ),
    },
    { title: '状态', dataIndex: 'isDisabled', render: (isDisabled: boolean) => (isDisabled ? <Tag color="red">已停用</Tag> : <Tag color="green">启用</Tag>) },
    {
      title: '名下用户 / 上限',
      key: 'users',
      render: (_: unknown, a: Agent) => (a.userCount > a.maxUsers ? <Typography.Text type="warning">{`${a.userCount} / ${a.maxUsers}（超出上限）`}</Typography.Text> : `${a.userCount} / ${a.maxUsers}`),
    },
    { title: '流量池已用 / 总量', key: 'pool', render: (_: unknown, a: Agent) => <PoolCell agent={a} /> },
    { title: '白名单套餐', dataIndex: 'planIds', render: (ids: number[]) => `${ids.length} 个` },
    { title: '两步验证', dataIndex: 'totpEnabled', render: (on: boolean) => (on ? <Tag color="blue">已开启</Tag> : <Tag>未开启</Tag>) },
    { title: '最近登录', dataIndex: 'lastLoginAt', render: (v: string | null | undefined) => formatTime(v) },
    {
      title: '操作',
      key: 'actions',
      render: (_: unknown, a: Agent) => (
        <Space wrap>
          <Button size="small" onClick={() => setViewing(a)}>
            开通记录
          </Button>
          {canWrite && (
            <>
              <Button size="small" onClick={() => openForm(a)}>
                编辑
              </Button>
              <Button size="small" onClick={() => setResetting(a)}>
                重置密码
              </Button>
              {a.totpEnabled && (
                <Popconfirm title={`清除 ${a.username} 的两步验证？`} description="用于对方遗失验证器设备；清除后其全部会话失效" onConfirm={() => clearTotp(a)}>
                  <Button size="small">清除两步验证</Button>
                </Popconfirm>
              )}
            </>
          )}
        </Space>
      ),
    },
  ]

  return (
    <>
      <PageHeader
        title="代理"
        action={canWrite && (
          <Button type="primary" icon={<PlusOutlined />} onClick={() => openForm(null)}>
            新建代理
          </Button>
        )}
      />
      <Typography.Paragraph type="secondary">
        代理只能管理自己名下的用户，受套餐白名单、用户数上限与流量池约束；每次开通扣减流量池并留下开通记录，作为线下结算的依据。
      </Typography.Paragraph>
      <PagedTable<Agent>
        load={loadAgents}
        columns={columns}
        refreshKey={refreshKey}
        filters={
          <Input.Search
            allowClear
            maxLength={MAX_KEYWORD_LEN}
            placeholder="按用户名搜索"
            aria-label="按用户名搜索"
            onSearch={(v) => setKeyword(v.trim())}
          />
        }
      />
      <AgentFormModal
        agent={editing}
        isOpen={isFormOpen}
        plans={plans.data ?? []}
        canReadPlans={canReadPlans}
        onClose={() => setIsFormOpen(false)}
        onSaved={() => {
          setIsFormOpen(false)
          reload()
        }}
      />
      <ResetCredentialModal
        target={resetting}
        rules={adminCredentialRules}
        okText="密码已重置，对方需重新登录"
        save={saveAgentCredential}
        onClose={() => setResetting(null)}
        onSaved={() => setResetting(null)}
      />
      <Drawer title={`开通记录：${viewing?.username ?? ''}`} open={viewing !== null} onClose={() => setViewing(null)} size="large" destroyOnHidden>
        {viewing !== null && <AllocationList agentId={viewing.id} />}
      </Drawer>
    </>
  )
}

// ---- 新建 / 编辑 ---------------------------------------------------------------------

interface AgentForm {
  username?: string
  credential?: string
  remark?: string
  maxUsers: number
  poolGb: number
  planIds: number[]
  isDisabled?: boolean
}

interface AgentFormModalProps {
  /** null = 新建。 */
  agent: Agent | null
  isOpen: boolean
  plans: Plan[]
  canReadPlans: boolean
  onClose: () => void
  onSaved: () => void
}

function AgentFormModal({ agent, isOpen, plans, canReadPlans, onClose, onSaved }: AgentFormModalProps) {
  return (
    <FormModal title={agent === null ? '新建代理' : `编辑代理：${agent.username}`} isOpen={isOpen} onClose={onClose} width={600}>
      {(generation) => <AgentFormBody key={generation} agent={agent} plans={plans} canReadPlans={canReadPlans} onSaved={onSaved} />}
    </FormModal>
  )
}

function formOf(agent: Agent | null): AgentForm {
  if (agent === null) {
    return { remark: '', maxUsers: 0, poolGb: 0, planIds: [] }
  }
  return { remark: agent.remark, maxUsers: agent.maxUsers, poolGb: bytesToGb(agent.trafficPoolTotal), planIds: agent.planIds, isDisabled: agent.isDisabled }
}

function AgentFormBody({ agent, plans, canReadPlans, onSaved }: Omit<AgentFormModalProps, 'isOpen' | 'onClose'>) {
  const { error, isBusy, run } = useSubmit(agent === null ? '已创建代理' : '已保存', onSaved)
  const initial = formOf(agent)
  // 只列流量与时长都有限的套餐；已在白名单里但后来改成不限的，标出来让超管移除（否则保存 400）
  const finitePlans = plans.filter(isWhitelistable)
  const planOptions = finitePlans.map((p) => ({ value: p.id, label: `${p.name}（${describeAgentPlan(p)}）` }))
  for (const id of initial.planIds) {
    if (!finitePlans.some((p) => p.id === id)) {
      const name = plans.find((p) => p.id === id)?.name ?? `#${id}`
      planOptions.push({ value: id, label: `${name}（已不符合条件，请移除）` })
    }
  }
  /** GB 回显有两位小数的舍入：没改就提交原字节数，免得悄悄改掉总量。 */
  const poolBytesOf = (gb: number | null | undefined) => (agent !== null && gb === initial.poolGb ? agent.trafficPoolTotal : gbToBytes(gb))
  const poolRules = [
    { required: true, message: '请填写流量池总量' },
    {
      validator: (_: unknown, gb: number | null | undefined) =>
        agent !== null && poolBytesOf(gb) < agent.trafficPoolUsed
          ? Promise.reject(new Error(`不能低于已用的 ${formatBytes(agent.trafficPoolUsed)}`))
          : Promise.resolve(),
    },
  ]

  const submit = (v: AgentForm) => {
    const base = { remark: v.remark ?? '', maxUsers: v.maxUsers ?? 0, trafficPoolTotal: poolBytesOf(v.poolGb), planIds: v.planIds ?? [] }
    if (agent === null) {
      const pw = v.credential ?? ''
      const body: AgentCreateInput = { ...base, username: v.username ?? '', password: pw }
      return run(() => api.POST('/api/admin/agents', { params: csrfHeader(), body }))
    }
    const body: AgentUpdateInput = { ...base, isDisabled: v.isDisabled ?? false }
    return run(() => api.PUT('/api/admin/agents/{id}', { params: { ...csrfHeader(), path: { id: agent.id } }, body }))
  }

  return (
    <>
      <ErrorAlert error={error} />
      <Form<AgentForm> layout="vertical" initialValues={initial} onFinish={(v) => void submit(v)}>
        {agent === null && (
          <>
            <Form.Item name="username" label="用户名" extra="与管理员共用一个命名空间" rules={adminUsernameRules}>
              <Input autoComplete="off" />
            </Form.Item>
            <Form.Item name="credential" label="初始密码" rules={adminCredentialRules}>
              <Input.Password autoComplete="new-password" />
            </Form.Item>
          </>
        )}
        <Form.Item name="remark" label="备注" rules={[{ max: MAX_REMARK_LEN }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item
          name="maxUsers"
          label="名下用户数上限"
          extra={agent === null ? '只计未删除的名下用户' : `当前 ${agent.userCount} 个；可以调到低于现有人数，之后不能再新增`}
          rules={[{ required: true, message: '请填写上限' }]}
        >
          <InputNumber min={0} max={MAX_AGENT_USERS} precision={0} className="full-width" aria-label="名下用户数上限" />
        </Form.Item>
        <Form.Item
          name="poolGb"
          label="流量池总量（GB）"
          extra={agent === null ? '累计制：每次开通按“套餐流量 × 重置周期数”扣减，扣完不能再开' : `已用 ${formatBytes(agent.trafficPoolUsed)}，总量不能低于已用`}
          rules={poolRules}
        >
          <InputNumber min={0} precision={GB_PRECISION} className="full-width" aria-label="流量池总量（GB）" />
        </Form.Item>
        <Form.Item
          name="planIds"
          label="可分配的套餐（白名单）"
          extra={canReadPlans ? '只列流量与时长都有限的套餐' : '没有套餐的查看权限，无法列出套餐'}
          rules={[{ type: 'array', max: MAX_WHITELIST_PLANS }]}
        >
          <Select mode="multiple" optionFilterProp="label" options={planOptions} aria-label="可分配的套餐" />
        </Form.Item>
        {agent !== null && (
          <Form.Item name="isDisabled" label="停用（其全部会话立即失效、邀请码不可用，名下用户不受影响）" valuePropName="checked">
            <Switch />
          </Form.Item>
        )}
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          {agent === null ? '创建' : '保存'}
        </Button>
      </Form>
    </>
  )
}

// ---- 开通记录 -----------------------------------------------------------------------

/** 某个代理的开通记录（分页、按时间范围），线下结算的依据（Ruling-103）。 */
function AllocationList({ agentId }: { agentId: number }) {
  const { from, to, picker } = useDateRangeFilter()
  const load = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) =>
      api.GET('/api/admin/agents/{id}/allocations', { params: { path: { id: agentId }, query: { page, pageSize, from, to } }, signal }),
    [agentId, from, to],
  )
  return (
    <PagedTable<AgentAllocation>
      load={load}
      columns={allocationColumns()}
      filters={picker}
    />
  )
}
