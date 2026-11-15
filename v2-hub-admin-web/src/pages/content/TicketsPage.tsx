import { Button, Drawer, Form, Input, InputNumber, Popconfirm, Select, Space, Spin, Tag, Typography, type TableProps } from 'antd'
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

const MAX_CONTENT_LEN = 5000
const REPLY_ROWS = 4

/** 工单状态的显示名与颜色；取值见 hub-api.yaml 的 TicketStatus。 */
const STATUS_META: Record<TicketStatus, { label: string; color?: string }> = {
  open: { label: '待回复', color: 'orange' },
  answered: { label: '已回复', color: 'green' },
  closed: { label: '已关闭' },
}
const STATUS_OPTIONS = (Object.keys(STATUS_META) as TicketStatus[]).map((value) => ({ value, label: STATUS_META[value].label }))

function StatusTag({ status }: { status: TicketStatus }) {
  return <Tag color={STATUS_META[status]?.color}>{STATUS_META[status]?.label ?? status}</Tag>
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

  const columns: TableProps<Ticket>['columns'] = [
    { title: '主题', dataIndex: 'subject' },
    { title: '用户', key: 'user', render: (_: unknown, t: Ticket) => `${t.username ?? ''} #${t.userId ?? ''}` },
    { title: '状态', dataIndex: 'status', render: (s: TicketStatus) => <StatusTag status={s} /> },
    { title: '最近回复', dataIndex: 'lastReplyAt', render: (v: string) => formatTime(v) },
    { title: '创建时间', dataIndex: 'createdAt', render: (v: string) => formatTime(v) },
    {
      title: '操作',
      key: 'actions',
      render: (_: unknown, t: Ticket) => (
        <Button size="small" onClick={() => setOpenedId(t.id)}>
          查看
        </Button>
      ),
    },
  ]

  return (
    <>
      <Typography.Title level={3}>工单</Typography.Title>
      <PagedTable<Ticket>
        load={loadTickets}
        columns={columns}
        refreshKey={refreshKey}
        filters={
          <>
            <Select
              allowClear
              placeholder="按状态过滤"
              aria-label="按状态过滤"
              className="filter-select"
              options={STATUS_OPTIONS}
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
      <Drawer title="工单详情" open={openedId !== null} onClose={() => setOpenedId(null)} size="large" destroyOnHidden>
        {openedId !== null && <TicketBody key={openedId} ticketId={openedId} canWrite={canWrite} onChanged={reload} />}
      </Drawer>
    </>
  )
}

interface TicketBodyProps {
  ticketId: number
  canWrite: boolean
  /** 回复或关闭后通知列表刷新。 */
  onChanged: () => void
}

function TicketBody({ ticketId, canWrite, onChanged }: TicketBodyProps) {
  const loadTicket = useCallback((signal: AbortSignal) => api.GET('/api/admin/tickets/{id}', { params: { path: { id: ticketId } }, signal }), [ticketId])
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
  const path = { id: ticketId }

  if (ticket.data === null) {
    return ticket.error === null ? <Spin /> : <ErrorAlert error={ticket.error} />
  }
  const detail = ticket.data
  return (
    <>
      <PageHeader
        title={detail.subject}
        level={5}
        action={<StatusTag status={detail.status} />}
      />
      <Typography.Paragraph type="secondary">
        {detail.username ?? ''} #{detail.userId ?? ''} · 创建于 {formatTime(detail.createdAt)}
      </Typography.Paragraph>
      <ErrorAlert error={ticket.error ?? reply.error ?? close.error} />
      {detail.messages.map((m) => (
        <MessageItem key={m.id} message={m} />
      ))}
      {canWrite && (
        <Form form={form} layout="vertical" onFinish={(v: { content: string }) => void reply.run(() => api.POST('/api/admin/tickets/{id}/messages', { params: { ...csrfHeader(), path }, body: { content: v.content } }))}>
          <Form.Item name="content" label="回复" extra="纯文本，按原样显示给用户" rules={[{ required: true, whitespace: true, max: MAX_CONTENT_LEN, message: `1–${MAX_CONTENT_LEN} 个字符` }]}>
            <Input.TextArea rows={REPLY_ROWS} showCount maxLength={MAX_CONTENT_LEN} />
          </Form.Item>
          <Space>
            <Button type="primary" htmlType="submit" loading={reply.isBusy}>
              发送回复
            </Button>
            {detail.status !== 'closed' && (
              <Popconfirm title="关闭这张工单？" description="用户再次回复会重新打开" onConfirm={() => close.run(() => api.POST('/api/admin/tickets/{id}/close', { params: { ...csrfHeader(), path } }))}>
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
function MessageItem({ message }: { message: TicketMessage }) {
  const isAdmin = message.authorKind === 'admin'
  return (
    <div className={isAdmin ? 'ticket-message ticket-message-admin' : 'ticket-message'}>
      <div className="ticket-message-meta">
        <Typography.Text strong>{isAdmin ? `管理员 ${message.authorName ?? ''}` : '用户'}</Typography.Text>
        <Typography.Text type="secondary">{formatTime(message.createdAt)}</Typography.Text>
      </div>
      <div className="ticket-message-content">{message.content}</div>
    </div>
  )
}
