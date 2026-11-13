import { PlusOutlined } from '@ant-design/icons'
import { Alert, App, Button, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Switch, Table, Tag, Typography, type TableProps } from 'antd'
import { useState } from 'react'
import { api, csrfHeader, type ApiError, type NodeGroup, type Plan, type PlanInput } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { useOpenGeneration } from '../../hooks/use-open-generation'
import { mutate, useRemote } from '../../hooks/use-remote'
import { PERMISSIONS } from '../../menu'
import { TABLE_SCROLL } from '../system/format'
import { RenewResetFields, type RenewResetForm } from './quota-fields'
import { bytesToGb, describeRenew, describeReset, formatBytes, gbToBytes, renewFieldsOf, renewModeOf, renewValueOf } from './quota'

const MAX_NAME_LEN = 64
const MAX_DURATION_DAYS = 36500
const MAX_LIMIT_IP = 1000

interface PlanForm extends RenewResetForm {
  name: string
  trafficGb: number
  durationDays: number
  startOnFirstUse: boolean
  limitIp: number
  nodeGroupIds: number[]
  allowClientChain: boolean
  allowCustomNode: boolean
  remark: string
}

const loadPlans = (signal: AbortSignal) => api.GET('/api/admin/plans', { signal })
const loadGroups = (signal: AbortSignal) => api.GET('/api/admin/node-groups', { signal })

function formOf(p: Plan | null): PlanForm {
  if (p === null) {
    return {
      name: '', trafficGb: 0, durationDays: 30, startOnFirstUse: false, limitIp: 0, nodeGroupIds: [], allowClientChain: false,
      allowCustomNode: false, remark: '', renewMode: 'none', renewValue: 1, renewMax: 0, trafficReset: 'never', trafficResetDay: 1,
    }
  }
  const renew = { renewEveryDays: p.renewEveryDays ?? 0, renewMonthDay: p.renewMonthDay ?? 0, renewWeekday: p.renewWeekday ?? 0 }
  return {
    name: p.name, trafficGb: bytesToGb(p.trafficLimit), durationDays: p.durationDays, startOnFirstUse: p.startOnFirstUse ?? false,
    limitIp: p.limitIp ?? 0, nodeGroupIds: p.nodeGroupIds, allowClientChain: p.allowClientChain ?? false, allowCustomNode: p.allowCustomNode ?? false,
    remark: p.remark ?? '', renewMode: renewModeOf(renew), renewValue: renewValueOf(renew), renewMax: p.renewMax ?? 0,
    trafficReset: p.trafficReset ?? 'never', trafficResetDay: p.trafficResetDay ?? 1,
  }
}

function inputOf(v: PlanForm): PlanInput {
  return {
    name: v.name, trafficLimit: gbToBytes(v.trafficGb), durationDays: v.durationDays ?? 0, startOnFirstUse: v.startOnFirstUse, limitIp: v.limitIp ?? 0,
    ...renewFieldsOf(v.renewMode, v.renewValue), renewMax: v.renewMode === 'none' ? 0 : (v.renewMax ?? 0),
    trafficReset: v.trafficReset, trafficResetDay: v.trafficResetDay ?? 1, allowClientChain: v.allowClientChain, allowCustomNode: v.allowCustomNode,
    remark: v.remark ?? '', nodeGroupIds: v.nodeGroupIds ?? [],
  }
}

/** 套餐管理（plan:read 查看，plan:write 增删改）；改套餐不影响已分配的用户（docs/03 §5.2）。 */
export function PlansPage() {
  const { can } = useSession()
  const { message } = App.useApp()
  const canWrite = can(PERMISSIONS.PLAN_WRITE)
  const plans = useRemote<Plan[]>(loadPlans)
  const groups = useRemote<NodeGroup[]>(loadGroups)
  const [editing, setEditing] = useState<Plan | null>(null)
  const [isOpen, setIsOpen] = useState(false)
  const groupName = new Map((groups.data ?? []).map((g) => [g.id, g.name]))

  const remove = async (p: Plan) => {
    const err = await mutate(() => api.DELETE('/api/admin/plans/{id}', { params: { ...csrfHeader(), path: { id: p.id } } }))
    if (err !== null) {
      void message.error(err.message)
      return
    }
    void message.success('已删除')
    plans.reload()
  }

  const columns: TableProps<Plan>['columns'] = [
    { title: '名称', dataIndex: 'name' },
    { title: '流量', dataIndex: 'trafficLimit', render: (v: number) => (v === 0 ? '不限' : formatBytes(v)) },
    { title: '时长', key: 'duration', render: (_: unknown, p: Plan) => (p.durationDays === 0 ? '不限' : `${p.durationDays} 天${p.startOnFirstUse ? '（首次使用起算）' : ''}`) },
    { title: '节点组', dataIndex: 'nodeGroupIds', render: (ids: number[]) => (ids.length === 0 ? '—' : ids.map((id) => <Tag key={id}>{groupName.get(id) ?? id}</Tag>)) },
    { title: 'IP 限制', dataIndex: 'limitIp', render: (v: number) => (v === 0 ? '不限' : v) },
    { title: '自动续期', key: 'renew', render: (_: unknown, p: Plan) => describeRenew({ renewEveryDays: p.renewEveryDays ?? 0, renewMonthDay: p.renewMonthDay ?? 0, renewWeekday: p.renewWeekday ?? 0, renewMax: p.renewMax ?? 0 }) },
    { title: '流量重置', key: 'reset', render: (_: unknown, p: Plan) => describeReset(p.trafficReset ?? 'never', p.trafficResetDay ?? 1) },
    { title: '用户数', dataIndex: 'userCount' },
  ]
  if (canWrite) {
    columns.push({
      title: '操作',
      key: 'actions',
      render: (_: unknown, p: Plan) => (
        <Space>
          <Button
            size="small"
            onClick={() => {
              setEditing(p)
              setIsOpen(true)
            }}
          >
            编辑
          </Button>
          <Popconfirm title={`删除套餐 ${p.name}？`} description="已分配的用户保留额度与到期，但失去节点授权" onConfirm={() => remove(p)}>
            <Button size="small" danger>
              删除
            </Button>
          </Popconfirm>
        </Space>
      ),
    })
  }

  return (
    <>
      <Space className="page-header">
        <Typography.Title level={3}>套餐</Typography.Title>
        {canWrite && (
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => {
              setEditing(null)
              setIsOpen(true)
            }}
          >
            新建套餐
          </Button>
        )}
      </Space>
      {plans.error !== null && <Alert type="error" title={plans.error.message} showIcon className="page-alert" />}
      <Table<Plan> rowKey="id" scroll={TABLE_SCROLL} loading={plans.isLoading} columns={columns} dataSource={plans.data ?? []} pagination={false} />
      <PlanModal
        plan={editing}
        isOpen={isOpen}
        groups={groups.data ?? []}
        onClose={() => setIsOpen(false)}
        onSaved={() => {
          setIsOpen(false)
          plans.reload()
        }}
      />
    </>
  )
}

interface PlanModalProps {
  plan: Plan | null
  isOpen: boolean
  groups: NodeGroup[]
  onClose: () => void
  onSaved: () => void
}

function PlanModal({ plan, isOpen, groups, onClose, onSaved }: PlanModalProps) {
  const generation = useOpenGeneration(isOpen)
  return (
    <Modal title={plan === null ? '新建套餐' : `编辑套餐：${plan.name}`} open={isOpen} onCancel={onClose} footer={null} destroyOnHidden width={620}>
      <PlanFormBody key={generation} plan={plan} groups={groups} onSaved={onSaved} />
    </Modal>
  )
}

function PlanFormBody({ plan, groups, onSaved }: { plan: Plan | null; groups: NodeGroup[]; onSaved: () => void }) {
  const { message } = App.useApp()
  const [form] = Form.useForm<PlanForm>()
  const [error, setError] = useState<ApiError | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const submit = async (v: PlanForm) => {
    setIsBusy(true)
    setError(null)
    const body = inputOf(v)
    const err = await mutate(() =>
      plan === null
        ? api.POST('/api/admin/plans', { params: csrfHeader(), body })
        : api.PUT('/api/admin/plans/{id}', { params: { ...csrfHeader(), path: { id: plan.id } }, body }),
    )
    setIsBusy(false)
    if (err !== null) {
      setError(err)
      return
    }
    void message.success(plan === null ? '已创建套餐' : '已保存（不影响已分配的用户）')
    onSaved()
  }

  return (
    <>
      {error !== null && <Alert type="error" title={error.message} showIcon className="page-alert" />}
      <Form<PlanForm> form={form} layout="vertical" initialValues={formOf(plan)} onFinish={submit}>
        <Form.Item name="name" label="名称" rules={[{ required: true, max: MAX_NAME_LEN, message: `1–${MAX_NAME_LEN} 个字符` }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="trafficGb" label="流量额度（GB）" extra="0 表示不限；上行 + 下行合计">
          <InputNumber min={0} className="full-width" />
        </Form.Item>
        <Form.Item name="durationDays" label="时长（天）" extra="0 表示不限">
          <InputNumber min={0} max={MAX_DURATION_DAYS} className="full-width" />
        </Form.Item>
        <Form.Item name="startOnFirstUse" label="首次使用后才开始计时" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name="nodeGroupIds" label="节点组">
          <Select mode="multiple" optionFilterProp="label" options={groups.map((g) => ({ value: g.id, label: g.name }))} />
        </Form.Item>
        <Form.Item name="limitIp" label="同时在线 IP 数" extra="0 表示不限；由各面板执行">
          <InputNumber min={0} max={MAX_LIMIT_IP} className="full-width" />
        </Form.Item>
        <RenewResetFields />
        <Form.Item name="allowClientChain" label="允许客户端链式代理（M6 起生效）" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name="allowCustomNode" label="允许导入自有落地节点（M6 起生效）" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name="remark" label="备注">
          <Input autoComplete="off" />
        </Form.Item>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          {plan === null ? '创建' : '保存'}
        </Button>
      </Form>
    </>
  )
}
