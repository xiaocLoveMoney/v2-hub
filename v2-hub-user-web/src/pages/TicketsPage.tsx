import { useState } from 'react'
import { Link, generatePath, useNavigate } from 'react-router'
import { api, csrfHeader, type Ticket } from '../api/client'
import { Alert, Button, Card, DataTable, Field, Modal, PageHeader, Pager, TextAreaField } from '../components/ui'
import { formatTime } from '../format'
import { request, usePagedRemote } from '../hooks/use-remote'
import { ROUTES } from '../menu'
import { MAX_TICKET_CONTENT_LEN, MAX_TICKET_SUBJECT_LEN, TicketStatusBadge } from './tickets'
import { useI18n } from '../i18n/i18n-context'

const PAGE_SIZE = 20
const CONTENT_ROWS = 6

const loadTickets = (page: number, signal: AbortSignal) => api.GET('/api/user/tickets', { params: { query: { page, pageSize: PAGE_SIZE } }, signal })

/** 工单支持（docs/03 §5.10）：本人的工单列表与新建；工单关闭时菜单不出现，接口也会拒绝。 */
export function TicketsPage() {
  const { t, tag } = useI18n()
  const [isCreating, setIsCreating] = useState(false)
  const tickets = usePagedRemote<Ticket>(loadTickets)
  const items = tickets.data?.items ?? []

  return (
    <>
      <PageHeader
        title={t('menu.tickets')}
        extra={
          <Button variant="primary" onClick={() => setIsCreating(true)}>
            {t('tickets.new')}
          </Button>
        }
      />
      <Card>
        {tickets.error !== null && <Alert tone="plain">{tickets.error.message}</Alert>}
        <DataTable headers={[t('tickets.colSubject'), t('tickets.colStatus'), t('tickets.colLastReply'), t('tickets.colCreated')]} isEmpty={tickets.data !== null && items.length === 0} emptyText={t('tickets.empty')}>
          {items.map((ticket) => (
            <tr key={ticket.id}>
              <td>
                <Link to={generatePath(ROUTES.TICKET_DETAIL, { id: String(ticket.id) })}>{ticket.subject}</Link>
              </td>
              <td>
                <TicketStatusBadge status={ticket.status} />
              </td>
              <td>{formatTime(ticket.lastReplyAt, tag)}</td>
              <td>{formatTime(ticket.createdAt, tag)}</td>
            </tr>
          ))}
        </DataTable>
        <Pager page={tickets.page} pageSize={PAGE_SIZE} total={tickets.data?.total ?? 0} onChange={tickets.setPage} />
      </Card>
      <CreateTicketModal isOpen={isCreating} onClose={() => setIsCreating(false)} />
    </>
  )
}

/** 新建工单：成功后直接进入这张工单的对话页。关闭时不渲染，表单状态随之重置。 */
function CreateTicketModal({ isOpen, onClose }: { isOpen: boolean; onClose: () => void }) {
  if (!isOpen) {
    return null
  }
  return <CreateTicketForm onClose={onClose} />
}

function CreateTicketForm({ onClose }: { onClose: () => void }) {
  const { t } = useI18n()
  const navigate = useNavigate()
  const [subject, setSubject] = useState('')
  const [content, setContent] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const handleCreate = async () => {
    setIsBusy(true)
    setError(null)
    try {
      const result = await request(() => api.POST('/api/user/tickets', { params: csrfHeader(), body: { subject: subject.trim(), content: content.trim() } }))
      if (result.error !== null) {
        setError(result.error.message)
        return
      }
      void navigate(generatePath(ROUTES.TICKET_DETAIL, { id: String(result.data.id) }))
    } finally {
      setIsBusy(false)
    }
  }

  return (
    <Modal
      title={t('tickets.new')}
      isOpen
      onClose={onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button variant="primary" isBusy={isBusy} disabled={subject.trim() === '' || content.trim() === ''} onClick={() => void handleCreate()}>
            {t('tickets.submit')}
          </Button>
        </>
      }
    >
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <Field label={t('tickets.subject')} maxLength={MAX_TICKET_SUBJECT_LEN} value={subject} onChange={(e) => setSubject(e.target.value)} />
      <TextAreaField
        label={t('tickets.content')}
        rows={CONTENT_ROWS}
        maxLength={MAX_TICKET_CONTENT_LEN}
        hint={t('tickets.contentHint')}
        value={content}
        onChange={(e) => setContent(e.target.value)}
      />
    </Modal>
  )
}
