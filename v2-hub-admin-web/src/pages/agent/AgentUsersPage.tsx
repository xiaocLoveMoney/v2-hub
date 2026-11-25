import { PlusOutlined } from '@ant-design/icons'
import { App, Button, Input, Popconfirm, Select, Space, Table, Tag, Typography, type TableProps } from 'antd'
import { useCallback, useState } from 'react'
import { api, csrfHeader, type AgentPlan, type AgentSummary, type AgentUser, type PageData, type UserStatus } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { PageHeader } from '../../components/PageHeader'
import { mutate, useRemote } from '../../hooks/use-remote'
import { formatMoney } from '../../money'
import { MAX_KEYWORD_LEN } from '../form-limits'
import { TABLE_SCROLL } from '../system/format'
import { USER_STATUS } from '../users/quota'
import { UserStatusTag } from '../users/quota-fields'
import { ExpiryCell, UsageCell } from '../users/user-cells'
import { loadAgentPlans, loadAgentSummary } from './agent-api'
import { withAgentErrors } from './agent-format'
import { AssignAgentPlanModal, CreateAgentUserModal, EditAgentUserModal, LowerQuotaModal, ResetAgentUserCredentialModal } from './AgentUserModals'

const DEFAULT_PAGE_SIZE = 20

type Dialog = 'create' | 'edit' | 'plan' | 'quota' | 'credential' | null

const STATUS_OPTIONS = (Object.keys(USER_STATUS) as UserStatus[]).map((s) => ({ value: s, label: USER_STATUS[s].label }))

/**
 * 代理的名下用户（docs/03 §5.23 代理能做的）：列表与筛选、新建（必选白名单套餐）、分配 / 续期、改备注与停用、
 * 调低额度 / 提前到期、重置密码、清除两步验证、重置订阅、删除。只调 /api/admin/agent/*（Ruling-101）。
 */
export function AgentUsersPage() {
  const { message, modal } = App.useApp()
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(DEFAULT_PAGE_SIZE)
  const [keyword, setKeyword] = useState('')
  const [status, setStatus] = useState<UserStatus | undefined>(undefined)
  const [planId, setPlanId] = useState<number | undefined>(undefined)
  const [dialog, setDialog] = useState<Dialog>(null)
  const [current, setCurrent] = useState<AgentUser | null>(null)

  const loadUsers = useCallback(
    (signal: AbortSignal) =>
      api.GET('/api/admin/agent/users', { params: { query: { page, pageSize, keyword: keyword === '' ? undefined : keyword, status, planId } }, signal }),
    [page, pageSize, keyword, status, planId],
  )
  const users = useRemote<PageData<AgentUser>>(loadUsers)
  const plans = useRemote<AgentPlan[]>(loadAgentPlans)
  const summary = useRemote<AgentSummary>(loadAgentSummary)

  const open = (d: Dialog, u: AgentUser | null) => {
    setCurrent(u)
    setDialog(d)
  }
  const close = () => setDialog(null)
  /** 写操作之后：用户列表与配额（池余量、用户数）都可能变了。 */
  const refresh = () => {
    users.reload()
    summary.reload()
  }
  const saved = () => {
    setDialog(null)
    refresh()
  }

  const runRow = async (action: () => Promise<{ error?: unknown }>, okText: string) => {
    const err = await mutate(withAgentErrors(action))
    if (err !== null) {
      void message.error(err.message)
      return
    }
    void message.success(okText)
    refresh()
  }

  const remove = (u: AgentUser) =>
    runRow(() => api.DELETE('/api/admin/agent/users/{id}', { params: { ...csrfHeader(), path: { id: u.id } } }), '已删除；面板不可达时会在恢复后删掉 client')

  const resetSubscription = (u: AgentUser) => {
    modal.confirm({
      title: `重置 ${u.username} 的订阅？`,
      content: '旧订阅地址与旧连接凭据立即失效，用户需要重新导入订阅',
      okText: '重置',
      okButtonProps: { danger: true },
      onOk: () => runRow(() => api.POST('/api/admin/agent/users/{id}/subscription/reset', { params: { ...csrfHeader(), path: { id: u.id } } }), '已重置，正在同步到各面板'),
    })
  }

  const clearTotp = (u: AgentUser) => {
    modal.confirm({
      title: `清除 ${u.username} 的两步验证？`,
      content: '该用户的全部登录会话会失效，之后登录不再需要验证码',
      okText: '清除',
      okButtonProps: { danger: true },
      onOk: () => runRow(() => api.DELETE('/api/admin/agent/users/{id}/totp', { params: { ...csrfHeader(), path: { id: u.id } } }), '已清除两步验证'),
    })
  }

  const columns: TableProps<AgentUser>['columns'] = [
    {
      title: '用户名',
      dataIndex: 'username',
      render: (name: string, u: AgentUser) => (
        <Space orientation="vertical" size={0}>
          {name}
          {u.remark !== '' && <Typography.Text type="secondary">{u.remark}</Typography.Text>}
        </Space>
      ),
    },
    { title: '邮箱', dataIndex: 'email', render: (v: string | null | undefined) => v ?? '—' },
    { title: '套餐', dataIndex: 'planName', render: (v: string) => v || '—' },
    { title: '状态', dataIndex: 'status', render: (s: UserStatus) => <UserStatusTag status={s} /> },
    { title: '已用 / 额度', key: 'usage', render: (_: unknown, u: AgentUser) => <UsageCell u={u} /> },
    { title: '到期', key: 'expiry', render: (_: unknown, u: AgentUser) => <ExpiryCell u={u} /> },
    { title: '余额', dataIndex: 'balance', render: (v: number) => formatMoney(v) },
    { title: '两步验证', dataIndex: 'totpEnabled', render: (on: boolean) => (on ? <Tag color="blue">已开启</Tag> : <Tag>未开启</Tag>) },
    {
      title: '操作',
      key: 'actions',
      render: (_: unknown, u: AgentUser) => (
        <Space wrap>
          <Button size="small" type="primary" disabled={u.isDisabled} onClick={() => open('plan', u)}>
            分配套餐
          </Button>
          <Button size="small" onClick={() => open('quota', u)}>
            调低额度
          </Button>
          <Button size="small" onClick={() => open('edit', u)}>
            编辑
          </Button>
          <Button size="small" onClick={() => open('credential', u)}>
            重置密码
          </Button>
          <Button size="small" onClick={() => resetSubscription(u)}>
            重置订阅
          </Button>
          {u.totpEnabled && (
            <Button size="small" onClick={() => clearTotp(u)}>
              清除两步验证
            </Button>
          )}
          <Popconfirm title={`删除用户 ${u.username}？`} description="会先删除各面板上的 client，不可恢复；已扣的流量池不退回" onConfirm={() => remove(u)}>
            <Button size="small" danger>
              删除
            </Button>
          </Popconfirm>
        </Space>
      ),
    },
  ]

  const planList = plans.data ?? []
  return (
    <>
      <PageHeader
        title="名下用户"
        action={
          <Button type="primary" icon={<PlusOutlined />} onClick={() => open('create', null)}>
            新建用户
          </Button>
        }
      />
      <Space wrap className="page-alert">
        <Input.Search
          allowClear
          maxLength={MAX_KEYWORD_LEN}
          placeholder="用户名、邮箱或备注"
          aria-label="按关键字搜索"
          onSearch={(v) => {
            setPage(1)
            setKeyword(v.trim())
          }}
        />
        <Select
          allowClear
          placeholder="按状态过滤"
          aria-label="按状态过滤"
          className="filter-select"
          value={status}
          options={STATUS_OPTIONS}
          onChange={(v: UserStatus | undefined) => {
            setPage(1)
            setStatus(v)
          }}
        />
        <Select
          allowClear
          placeholder="按套餐过滤"
          aria-label="按套餐过滤"
          className="filter-select"
          value={planId}
          options={planList.map((p) => ({ value: p.id, label: p.name }))}
          onChange={(v: number | undefined) => {
            setPage(1)
            setPlanId(v)
          }}
        />
      </Space>
      <ErrorAlert error={users.error ?? plans.error} />
      <Table<AgentUser>
        rowKey="id"
        scroll={TABLE_SCROLL}
        loading={users.isLoading}
        columns={columns}
        dataSource={users.data?.items ?? []}
        pagination={{
          current: page,
          pageSize,
          total: users.data?.total ?? 0,
          onChange: (p, size) => {
            setPage(p)
            setPageSize(size)
          },
        }}
      />
      <CreateAgentUserModal isOpen={dialog === 'create'} plans={planList} summary={summary.data} onClose={close} onSaved={saved} />
      <AssignAgentPlanModal user={dialog === 'plan' ? current : null} plans={planList} summary={summary.data} onClose={close} onSaved={saved} />
      <EditAgentUserModal user={dialog === 'edit' ? current : null} onClose={close} onSaved={saved} />
      <LowerQuotaModal user={dialog === 'quota' ? current : null} onClose={close} onSaved={saved} />
      <ResetAgentUserCredentialModal user={dialog === 'credential' ? current : null} onClose={close} onSaved={saved} />
    </>
  )
}
