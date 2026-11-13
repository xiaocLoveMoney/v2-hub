import { PlusOutlined } from '@ant-design/icons'
import { Alert, App, Button, Input, Popconfirm, Progress, Select, Space, Table, Tag, Tooltip, Typography, type TableProps } from 'antd'
import { useCallback, useState } from 'react'
import { api, csrfHeader, type EndUser, type PageData, type Plan, type UserStatus } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { mutate, useRemote } from '../../hooks/use-remote'
import { PERMISSIONS } from '../../menu'
import { TABLE_SCROLL, formatTime } from '../system/format'
import { UserStatusTag } from './quota-fields'
import { USER_STATUS, formatBytes } from './quota'
import { AssignPlanModal, CreateUserModal, EditUserModal, ProvisionDrawer, QuotaModal, ResetUserCredentialModal } from './UserModals'

const DEFAULT_PAGE_SIZE = 20
const PERCENT_FULL = 100
const loadPlans = (signal: AbortSignal) => api.GET('/api/admin/plans', { signal })

type Dialog = 'create' | 'edit' | 'plan' | 'quota' | 'credential' | 'provisions' | null

function UsageCell({ u }: { u: EndUser }) {
  if (u.trafficLimit === 0) {
    return <span>{formatBytes(u.trafficUsed)} / 不限</span>
  }
  const percent = Math.min(PERCENT_FULL, Math.round((u.trafficUsed / u.trafficLimit) * PERCENT_FULL))
  return (
    <Tooltip title={`${formatBytes(u.trafficUsed)} / ${formatBytes(u.trafficLimit)}`}>
      <Progress percent={percent} size="small" status={percent >= PERCENT_FULL ? 'exception' : 'normal'} className="usage-bar" />
    </Tooltip>
  )
}

function ExpiryCell({ u }: { u: EndUser }) {
  if (u.expiresAt !== null && u.expiresAt !== undefined) {
    return <span>{formatTime(u.expiresAt)}</span>
  }
  return u.pendingDays > 0 ? <span>首次使用后 {u.pendingDays} 天</span> : <span>不过期</span>
}

function ProvisionCell({ u }: { u: EndUser }) {
  const p = u.provisions
  if (p.synced + p.pending + p.failed === 0) {
    return <Typography.Text type="secondary">未下发</Typography.Text>
  }
  return (
    <Space size={4}>
      {p.synced > 0 && <Tag color="green">已同步 {p.synced}</Tag>}
      {p.pending > 0 && <Tag color="blue">待同步 {p.pending}</Tag>}
      {p.failed > 0 && <Tag color="red">失败 {p.failed}</Tag>}
    </Space>
  )
}

/** 用户管理（user:read 查看，user:write 增改、续费、改用量、停用、删除；docs/03 §5.2）。 */
export function UsersPage() {
  const { can } = useSession()
  const { message, modal } = App.useApp()
  const canWrite = can(PERMISSIONS.USER_WRITE)
  const canReadPlans = can(PERMISSIONS.PLAN_READ)
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(DEFAULT_PAGE_SIZE)
  const [keyword, setKeyword] = useState('')
  const [status, setStatus] = useState<UserStatus | undefined>(undefined)
  const [planId, setPlanId] = useState<number | undefined>(undefined)
  const [dialog, setDialog] = useState<Dialog>(null)
  const [current, setCurrent] = useState<EndUser | null>(null)

  const loadUsers = useCallback(
    (signal: AbortSignal) =>
      api.GET('/api/admin/users', { params: { query: { page, pageSize, keyword: keyword === '' ? undefined : keyword, status, planId } }, signal }),
    [page, pageSize, keyword, status, planId],
  )
  const users = useRemote<PageData<EndUser>>(loadUsers)
  const loadPlansIfAllowed = useCallback((signal: AbortSignal) => (canReadPlans ? loadPlans(signal) : Promise.resolve({ data: { data: [] as Plan[] } })), [canReadPlans])
  const plans = useRemote<Plan[]>(loadPlansIfAllowed)

  const open = (d: Dialog, u: EndUser | null) => {
    setCurrent(u)
    setDialog(d)
  }
  const close = () => setDialog(null)
  const saved = () => {
    setDialog(null)
    users.reload()
  }

  const remove = async (u: EndUser) => {
    const err = await mutate(() => api.DELETE('/api/admin/users/{id}', { params: { ...csrfHeader(), path: { id: u.id } } }))
    if (err !== null) {
      void message.error(err.message)
      return
    }
    void message.success('已删除；面板不可达时会在恢复后删掉 client')
    users.reload()
  }

  /** 重置订阅：换订阅地址并轮换连接凭据（docs/03 §5.4 重置）。 */
  const resetSubscription = (u: EndUser) => {
    modal.confirm({
      title: `重置 ${u.username} 的订阅？`,
      content: '旧订阅地址与旧连接凭据立即失效，用户需要重新导入订阅',
      okText: '重置',
      okButtonProps: { danger: true },
      onOk: async () => {
        const err = await mutate(() => api.POST('/api/admin/users/{id}/subscription/reset', { params: { ...csrfHeader(), path: { id: u.id } } }))
        if (err !== null) {
          void message.error(err.message)
          return
        }
        void message.success('已重置，正在同步到各面板')
        users.reload()
      },
    })
  }

  /** 清除两步验证：用户遗失设备时用，其全部会话失效（docs/03 §5.3）。 */
  const clearTotp = (u: EndUser) => {
    modal.confirm({
      title: `清除 ${u.username} 的两步验证？`,
      content: '该用户的全部登录会话会失效，之后登录不再需要验证码',
      okText: '清除',
      okButtonProps: { danger: true },
      onOk: async () => {
        const err = await mutate(() => api.DELETE('/api/admin/users/{id}/totp', { params: { ...csrfHeader(), path: { id: u.id } } }))
        if (err !== null) {
          void message.error(err.message)
          return
        }
        void message.success('已清除两步验证')
        users.reload()
      },
    })
  }

  const columns: TableProps<EndUser>['columns'] = [
    {
      title: '用户名',
      dataIndex: 'username',
      render: (name: string, u: EndUser) => (
        <Space orientation="vertical" size={0}>
          {name}
          {u.remark !== '' && <Typography.Text type="secondary">{u.remark}</Typography.Text>}
        </Space>
      ),
    },
    {
      title: '邮箱',
      key: 'email',
      render: (_: unknown, u: EndUser) =>
        u.email === null || u.email === undefined ? (
          '—'
        ) : (
          <Space size={4}>
            {u.email}
            {u.isEmailVerified !== true && <Tag>未验证</Tag>}
          </Space>
        ),
    },
    { title: '套餐', dataIndex: 'planName', render: (v: string) => v || '—' },
    { title: '邀请人', dataIndex: 'invitedByUsername', render: (v: string | undefined) => v || '—' },
    { title: '状态', dataIndex: 'status', render: (s: UserStatus) => <UserStatusTag status={s} /> },
    { title: '已用 / 额度', key: 'usage', render: (_: unknown, u: EndUser) => <UsageCell u={u} /> },
    { title: '到期', key: 'expiry', render: (_: unknown, u: EndUser) => <ExpiryCell u={u} /> },
    { title: '下发', key: 'provisions', render: (_: unknown, u: EndUser) => <ProvisionCell u={u} /> },
    {
      title: '操作',
      key: 'actions',
      render: (_: unknown, u: EndUser) => (
        <Space wrap>
          <Button size="small" onClick={() => open('provisions', u)}>
            下发详情
          </Button>
          {canWrite && (
            <>
              <Button size="small" type="primary" disabled={u.isDisabled} onClick={() => open('plan', u)}>
                续费
              </Button>
              <Button size="small" onClick={() => open('quota', u)}>
                改用量
              </Button>
              <Button size="small" onClick={() => open('edit', u)}>
                编辑
              </Button>
              <Button size="small" onClick={() => open('credential', u)}>
                重置口令
              </Button>
              <Button size="small" onClick={() => resetSubscription(u)}>
                重置订阅
              </Button>
              {u.totpEnabled === true && (
                <Button size="small" onClick={() => clearTotp(u)}>
                  清除两步验证
                </Button>
              )}
              <Popconfirm title={`删除用户 ${u.username}？`} description="会先删除各面板上的 client，不可恢复" onConfirm={() => remove(u)}>
                <Button size="small" danger>
                  删除
                </Button>
              </Popconfirm>
            </>
          )}
        </Space>
      ),
    },
  ]

  const planOptions = (plans.data ?? []).map((p) => ({ value: p.id, label: p.name }))
  return (
    <>
      <Space className="page-header">
        <Typography.Title level={3}>用户</Typography.Title>
        {canWrite && (
          <Button type="primary" icon={<PlusOutlined />} onClick={() => open('create', null)}>
            新建用户
          </Button>
        )}
      </Space>
      <Space wrap className="page-alert">
        <Input.Search
          allowClear
          placeholder="用户名或备注"
          onSearch={(v) => {
            setPage(1)
            setKeyword(v.trim())
          }}
        />
        <Select
          allowClear
          placeholder="按状态过滤"
          className="filter-select"
          value={status}
          options={(Object.keys(USER_STATUS) as UserStatus[]).map((s) => ({ value: s, label: USER_STATUS[s].label }))}
          onChange={(v: UserStatus | undefined) => {
            setPage(1)
            setStatus(v)
          }}
        />
        {canReadPlans && (
          <Select
            allowClear
            placeholder="按套餐过滤"
            className="filter-select"
            value={planId}
            options={planOptions}
            onChange={(v: number | undefined) => {
              setPage(1)
              setPlanId(v)
            }}
          />
        )}
      </Space>
      {users.error !== null && <Alert type="error" title={users.error.message} showIcon className="page-alert" />}
      <Table<EndUser>
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
      <CreateUserModal isOpen={dialog === 'create'} plans={plans.data ?? []} onClose={close} onSaved={saved} />
      <EditUserModal user={dialog === 'edit' ? current : null} onClose={close} onSaved={saved} />
      <AssignPlanModal user={dialog === 'plan' ? current : null} plans={plans.data ?? []} onClose={close} onSaved={saved} />
      <QuotaModal user={dialog === 'quota' ? current : null} onClose={close} onSaved={saved} />
      <ResetUserCredentialModal user={dialog === 'credential' ? current : null} onClose={close} onSaved={saved} />
      <ProvisionDrawer user={dialog === 'provisions' ? current : null} canWrite={canWrite} onClose={close} onChanged={() => users.reload()} />
    </>
  )
}
