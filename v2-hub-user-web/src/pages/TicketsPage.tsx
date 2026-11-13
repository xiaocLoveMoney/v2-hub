import { useCallback, useState } from 'react'
import { Link, generatePath, useNavigate } from 'react-router'
import { api, csrfHeader, toApiError, type PageData, type Ticket } from '../api/client'
import { Alert, Button, Card, Empty, Field, Modal, PageHeader, Pager, TextAreaField } from '../components/ui'
import { formatTime } from '../format'
import { useRemote } from '../hooks/use-remote'
import { ROUTES } from '../menu'
import { MAX_TICKET_CONTENT_LEN, MAX_TICKET_SUBJECT_LEN, TicketStatusBadge } from './tickets'
import { useI18n } from '../i18n/i18n-context'

const PAGE_SIZE = 20
const CONTENT_ROWS = 6

/** 工单支持（docs/03 §5.10）：本人的工单列表与新建；工单关闭时菜单不出现，接口也会拒绝。 */
export function TicketsPage() {
  const { t, tag } = useI18n()
  const [page, setPage] = useState(1)
  const [isCreating, setIsCreating] = useState(false)
  const load = useCallback((signal: AbortSignal) => api.GET('/api/user/tickets', { params: { query: { page, pageSize: PAGE_SIZE } }, signal }), [page])
  const tickets = useRemote<PageData<Ticket>>(load)
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
        <div className="hub-table-wrap">
          <table className="hub-table">
            <thead>
              <tr>
                <th>{t('tickets.colSubject')}</th>
                <th>{t('tickets.colStatus')}</th>
                <th>{t('tickets.colLastReply')}</th>
                <th>{t('tickets.colCreated')}</th>
              </tr>
            </thead>
            <tbody>
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
            </tbody>
          </table>
        </div>
        {tickets.data !== null && items.length === 0 && <Empty>{t('tickets.empty')}</Empty>}
        <Pager page={page} pageSize={PAGE_SIZE} total={tickets.data?.total ?? 0} onChange={setPage} />
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
      const res = await api.POST('/api/user/tickets', { params: csrfHeader(), body: { subject: subject.trim(), content: content.trim() } })
      if (res.error !== undefined || res.data?.data === undefined) {
        setError(toApiError(res.error).message)
        return
      }
      void navigate(generatePath(ROUTES.TICKET_DETAIL, { id: String(res.data.data.id) }))
    } catch (err) {
      reportError(err)
      setError(toApiError(err).message)
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
