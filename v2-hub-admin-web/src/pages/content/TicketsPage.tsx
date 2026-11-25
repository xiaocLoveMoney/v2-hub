import { Button, Drawer, Form, Input, InputNumber, Popconfirm, Select, Space, Spin, Typography } from 'antd'
import { useCallback, useState } from 'react'
import { api, csrfHeader, type Ticket, type TicketDetail, type TicketMessage, type TicketStatus } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { ErrorAlert } from '../../components/ErrorAlert'
import { PagedTable } from '../../components/PagedTable'
import { useRemote } from '../../hooks/use-remote'
import { useSubmit } from '../../hooks/use-submit'
import { PERMISSIONS } from '../../menu'
import { formatTime } from '../system/format'
import { PageHeader } from '../../components/PageHeader'
import { TICKET_STATUS_OPTIONS, renderTicketStatus, ticketColumns } from './ticket-columns'

const MAX_CONTENT_LEN = 5000
const REPLY_ROWS = 4

/** openapi-fetch 返回的最小形状。 */
interface TicketResult {
  data?: { data?: TicketDetail }
  error?: unknown
}

/** 工单详情用到的三个接口；员工与代理各给一套（代理走 /api/admin/agent/tickets，Ruling-101）。 */
export interface TicketEndpoints {
  get: (id: number, signal: AbortSignal) => Promise<TicketResult>
  reply: (id: number, content: string) => Promise<{ error?: unknown }>
  close: (id: number) => Promise<{ error?: unknown }>
}

/** 员工的工单接口。 */
const STAFF_TICKET_ENDPOINTS: TicketEndpoints = {
  get: (id, signal) => api.GET('/api/admin/tickets/{id}', { params: { path: { id } }, signal }),
  reply: (id, content) => api.POST('/api/admin/tickets/{id}/messages', { params: { ...csrfHeader(), path: { id } }, body: { content } }),
  close: (id) => api.POST('/api/admin/tickets/{id}/close', { params: { ...csrfHeader(), path: { id } } }),
}

/** 员工视角下管理端回复的署名。 */
const staffAuthorLabel = (m: TicketMessage) => `管理员 ${m.authorName ?? ''}`

interface TicketDrawerProps {
  /** null = 关闭。 */
  ticketId: number | null
  endpoints: TicketEndpoints
  canWrite: boolean
  /** 管理端回复的署名。 */
  authorLabel: (message: TicketMessage) => string
  onClose: () => void
  /** 回复或关闭后通知列表刷新。 */
  onChanged: () => void
}

/** 工单详情抽屉：按时间列消息，可回复与关闭。 */
export function TicketDrawer({ ticketId, endpoints, canWrite, authorLabel, onClose, onChanged }: TicketDrawerProps) {
  return (
    <Drawer title="工单详情" open={ticketId !== null} onClose={onClose} size="large" destroyOnHidden>
      {ticketId !== null && (
        <TicketBody key={ticketId} ticketId={ticketId} endpoints={endpoints} canWrite={canWrite} authorLabel={authorLabel} onChanged={onChanged} />
      )}
    </Drawer>
  )
}

/** 工单（ticket:read 查看，ticket:write 回复与关闭；docs/03 §5.10）。 */
export function TicketsPage() {
  const { can } = useSession()
  const canWrite = can(PERMISSIONS.TICKET_WRITE)
  const [status, setStatus] = useState<TicketStatus | undefined>(undefined)
  const [userId, setUserId] = useState<number | undefined>(undefined)
  const [openedId, setOpenedId] = useState<number | null>(null)
  const [refreshKey, setRefreshKey] = useState(0)
  const reload = () => setRefreshKey((k) => k + 1)
  const loadTickets = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) =>
      api.GET('/api/admin/tickets', { params: { query: { page, pageSize, status, userId } }, signal }),
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
            <InputNumber
              min={1}
              precision={0}
              placeholder="按用户 ID 过滤"
              aria-label="按用户 ID 过滤"
              className="filter-select"
              value={userId}
              onChange={(v) => setUserId(v === null ? undefined : v)}
            />
          </>
        }
      />
      <TicketDrawer
        ticketId={openedId}
        endpoints={STAFF_TICKET_ENDPOINTS}
        canWrite={canWrite}
        authorLabel={staffAuthorLabel}
        onClose={() => setOpenedId(null)}
        onChanged={reload}
      />
    </>
  )
}

type TicketBodyProps = Omit<TicketDrawerProps, 'ticketId' | 'onClose'> & { ticketId: number }

function TicketBody({ ticketId, endpoints, canWrite, authorLabel, onChanged }: TicketBodyProps) {
  const loadTicket = useCallback((signal: AbortSignal) => endpoints.get(ticketId, signal), [endpoints, ticketId])
  const ticket = useRemote<TicketDetail>(loadTicket)
  const [form] = Form.useForm<{ content: string }>()
  const refresh = () => {
    ticket.reload()
    onChanged()
  }
  const reply = useSubmit('已回复', () => {
    form.resetFields()
    refresh()
  })
  const close = useSubmit('已关闭', refresh)

  if (ticket.data === null) {
    return ticket.error === null ? <Spin /> : <ErrorAlert error={ticket.error} />
  }
  const detail = ticket.data
  return (
    <>
      <PageHeader
        title={detail.subject}
        level={5}
        action={renderTicketStatus(detail.status)}
      />
      <Typography.Paragraph type="secondary">
        {detail.username ?? ''} #{detail.userId ?? ''} · 创建于 {formatTime(detail.createdAt)}
      </Typography.Paragraph>
      <ErrorAlert error={ticket.error ?? reply.error ?? close.error} />
      {detail.messages.map((m) => (
        <MessageItem key={m.id} message={m} authorLabel={authorLabel} />
      ))}
      {canWrite && (
        <Form form={form} layout="vertical" onFinish={(v: { content: string }) => void reply.run(() => endpoints.reply(ticketId, v.content))}>
          <Form.Item name="content" label="回复" extra="纯文本，按原样显示给用户" rules={[{ required: true, whitespace: true, max: MAX_CONTENT_LEN, message: `1–${MAX_CONTENT_LEN} 个字符` }]}>
            <Input.TextArea rows={REPLY_ROWS} showCount maxLength={MAX_CONTENT_LEN} />
          </Form.Item>
          <Space>
            <Button type="primary" htmlType="submit" loading={reply.isBusy}>
              发送回复
            </Button>
            {detail.status !== 'closed' && (
              <Popconfirm title="关闭这张工单？" description="用户再次回复会重新打开" onConfirm={() => close.run(() => endpoints.close(ticketId))}>
                <Button loading={close.isBusy}>关闭工单</Button>
              </Popconfirm>
            )}
          </Space>
        </Form>
      )}
    </>
  )
}

/** 一条消息：内容是用户输入的纯文本，交给 React 转义后按原样（保留换行）显示，不走 innerHTML。 */
function MessageItem({ message, authorLabel }: { message: TicketMessage; authorLabel: (message: TicketMessage) => string }) {
  const isAdmin = message.authorKind === 'admin'
  return (
    <div className={isAdmin ? 'ticket-message ticket-message-admin' : 'ticket-message'}>
      <div className="ticket-message-meta">
        <Typography.Text strong>{isAdmin ? authorLabel(message) : '用户'}</Typography.Text>
        <Typography.Text type="secondary">{formatTime(message.createdAt)}</Typography.Text>
      </div>
      <div className="ticket-message-content">{message.content}</div>
    </div>
  )
}
