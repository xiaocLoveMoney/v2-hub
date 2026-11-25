import { PlusOutlined } from '@ant-design/icons'
import { Button, DatePicker, Popconfirm, Select, Space, Typography } from 'antd'
import type { Dayjs } from 'dayjs'
import { useCallback, useState } from 'react'
import {
  api,
  csrfHeader,
  type InviteCode,
  type LedgerEntry,
  type LedgerKind,
  type Order,
  type OrderKind,
  type Ticket,
  type TicketMessage,
  type TicketStatus,
} from '../../api/client'
import { PagedTable } from '../../components/PagedTable'
import { PageHeader } from '../../components/PageHeader'
import { useRowAction } from '../../hooks/use-submit'
import { DEFAULT_CURRENCY } from '../../money'
import { TicketDrawer, type TicketEndpoints } from '../content/TicketsPage'
import { TICKET_STATUS_OPTIONS, ticketColumns } from '../content/ticket-columns'
import { UserIdFilter } from '../shop/RecordPages'
import { LEDGER_KIND_OPTIONS, ORDER_KIND_OPTIONS, ledgerColumns, orderColumns } from '../shop/record-columns'
import { InviteCodeModal, type InviteCodeSave } from '../users/InviteCodesPage'
import { inviteCodeBaseColumns } from '../users/invite-code-columns'
import { withAgentErrors } from './agent-format'

// 代理的邀请码、工单、订单、余额流水（docs/03 §5.23）：表格与表单沿用员工页面的，接口换成 /api/admin/agent/*（Ruling-101）。
// 金额按默认币种显示：代理读不到系统设置里的显示币种（见《任务与边界》契约缺口）。

/** 代理自己的码：新建由服务端生成，经它注册的用户归自己（Ruling-102）。 */
const saveAsAgent: InviteCodeSave = (code, body) =>
  withAgentErrors(() =>
    code === null
      ? api.POST('/api/admin/agent/invite-codes', { params: csrfHeader(), body })
      : api.PUT('/api/admin/agent/invite-codes/{id}', { params: { ...csrfHeader(), path: { id: code.id } }, body }),
  )()

/** 代理的邀请码：增改删自己的码。 */
export function AgentInviteCodesPage() {
  const [editing, setEditing] = useState<InviteCode | null>(null)
  const [isOpen, setIsOpen] = useState(false)
  const [refreshKey, setRefreshKey] = useState(0)
  const reload = () => setRefreshKey((k) => k + 1)
  const act = useRowAction(reload)
  const loadCodes = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) => api.GET('/api/admin/agent/invite-codes', { params: { query: { page, pageSize } }, signal }),
    [],
  )
  const open = (code: InviteCode | null) => {
    setEditing(code)
    setIsOpen(true)
  }
  const columns = inviteCodeBaseColumns()
  columns.push({
    title: '操作',
    key: 'actions',
    render: (_: unknown, c: InviteCode) => (
      <Space>
        <Button size="small" onClick={() => open(c)}>
          编辑
        </Button>
        <Popconfirm
          title={`删除邀请码 ${c.code}？`}
          description="已用它注册的用户仍归你名下"
          onConfirm={() => act(withAgentErrors(() => api.DELETE('/api/admin/agent/invite-codes/{id}', { params: { ...csrfHeader(), path: { id: c.id } } })), '已删除')}
        >
          <Button size="small" danger>
            删除
          </Button>
        </Popconfirm>
      </Space>
    ),
  })
  return (
    <>
      <PageHeader
        title="邀请码"
        action={
          <Button type="primary" icon={<PlusOutlined />} onClick={() => open(null)}>
            新建邀请码
          </Button>
        }
      />
      <Typography.Paragraph type="secondary">经你的邀请码注册的用户归你名下、计入用户数上限；注册时不送赠送套餐，需要你分配。</Typography.Paragraph>
      <PagedTable<InviteCode> load={loadCodes} columns={columns} refreshKey={refreshKey} />
      <InviteCodeModal
        code={editing}
        isOpen={isOpen}
        save={saveAsAgent}
        onClose={() => setIsOpen(false)}
        onSaved={() => {
          setIsOpen(false)
          reload()
        }}
      />
    </>
  )
}

/** 代理侧的工单接口。 */
const AGENT_TICKET_ENDPOINTS: TicketEndpoints = {
  get: (id, signal) => api.GET('/api/admin/agent/tickets/{id}', { params: { path: { id } }, signal }),
  reply: (id, content) =>
    withAgentErrors(() => api.POST('/api/admin/agent/tickets/{id}/messages', { params: { ...csrfHeader(), path: { id } }, body: { content } }))(),
  close: (id) => withAgentErrors(() => api.POST('/api/admin/agent/tickets/{id}/close', { params: { ...csrfHeader(), path: { id } } }))(),
}

/** 代理视角下管理端回复的署名：自己的回复带自己的用户名，平台员工的回复不带账号名（docs/03 §5.23 不暴露员工）。 */
const agentAuthorLabel = (m: TicketMessage) => (m.authorName ? `代理 ${m.authorName}` : '平台客服')

/** 代理的工单：名下用户的工单，查看、回复、关闭。 */
export function AgentTicketsPage() {
  const [status, setStatus] = useState<TicketStatus | undefined>(undefined)
  const [userId, setUserId] = useState<number | undefined>(undefined)
  const [openedId, setOpenedId] = useState<number | null>(null)
  const [refreshKey, setRefreshKey] = useState(0)
  const loadTickets = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) => api.GET('/api/admin/agent/tickets', { params: { query: { page, pageSize, status, userId } }, signal }),
    [status, userId],
  )
  return (
    <>
      <Typography.Title level={3}>工单</Typography.Title>
      <PagedTable<Ticket>
        load={loadTickets}
        columns={ticketColumns(setOpenedId)}
        refreshKey={refreshKey}
        filters={
          <>
            <Select
              allowClear
              placeholder="按状态过滤"
              aria-label="按状态过滤"
              className="filter-select"
              options={TICKET_STATUS_OPTIONS}
              value={status}
              onChange={(v: TicketStatus | undefined) => setStatus(v)}
            />
            <UserIdFilter value={userId} onChange={setUserId} />
          </>
        }
      />
      <TicketDrawer
        ticketId={openedId}
        endpoints={AGENT_TICKET_ENDPOINTS}
        canWrite
        authorLabel={agentAuthorLabel}
        onClose={() => setOpenedId(null)}
        onChanged={() => setRefreshKey((k) => k + 1)}
      />
    </>
  )
}

type Range = [Dayjs | null, Dayjs | null] | null

/** 代理的订单：名下（未删除）用户的订单，只读。 */
export function AgentOrdersPage() {
  const [userId, setUserId] = useState<number | undefined>(undefined)
  const [kind, setKind] = useState<OrderKind | undefined>(undefined)
  const [range, setRange] = useState<Range>(null)
  const from = range?.[0]?.startOf('day').toISOString()
  const to = range?.[1]?.add(1, 'day').startOf('day').toISOString()
  const load = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) =>
      api.GET('/api/admin/agent/orders', { params: { query: { page, pageSize, userId, kind, from, to } }, signal }),
    [userId, kind, from, to],
  )
  return (
    <>
      <Typography.Title level={3}>订单</Typography.Title>
      <PagedTable<Order>
        load={load}
        columns={orderColumns(DEFAULT_CURRENCY)}
        filters={
          <>
            <UserIdFilter value={userId} onChange={setUserId} />
            <Select allowClear placeholder="按类型过滤" aria-label="按类型过滤" className="filter-select" options={ORDER_KIND_OPTIONS} value={kind} onChange={(v: OrderKind | undefined) => setKind(v)} />
            <DatePicker.RangePicker allowClear aria-label="按时间范围过滤" value={range} onChange={(v) => setRange(v)} />
          </>
        }
      />
    </>
  )
}

/** 代理的余额流水：名下（未删除）用户的流水，只读；不显示操作人（接口恒为空）。 */
export function AgentLedgerPage() {
  const [userId, setUserId] = useState<number | undefined>(undefined)
  const [kind, setKind] = useState<LedgerKind | undefined>(undefined)
  const load = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) => api.GET('/api/admin/agent/wallet-ledger', { params: { query: { page, pageSize, userId, kind } }, signal }),
    [userId, kind],
  )
  return (
    <>
      <Typography.Title level={3}>余额流水</Typography.Title>
      <PagedTable<LedgerEntry>
        load={load}
        columns={ledgerColumns(DEFAULT_CURRENCY, false)}
        filters={
          <>
            <UserIdFilter value={userId} onChange={setUserId} />
            <Select allowClear placeholder="按类型过滤" aria-label="按类型过滤" className="filter-select" options={LEDGER_KIND_OPTIONS} value={kind} onChange={(v: LedgerKind | undefined) => setKind(v)} />
          </>
        }
      />
    </>
  )
}
