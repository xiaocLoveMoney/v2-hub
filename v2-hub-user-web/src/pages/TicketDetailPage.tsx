import { useCallback, useState } from 'react'
import { Link, useParams } from 'react-router'
import { api, csrfHeader, type TicketDetail, type TicketMessage } from '../api/client'
import { useToast } from '../components/toast-context'
import { Alert, Button, Card, Confirm, Loading, PageHeader, TextAreaField } from '../components/ui'
import { formatTime } from '../format'
import { mutate, useRemote } from '../hooks/use-remote'
import { ROUTES } from '../menu'
import { NotFoundPage } from './NotFoundPage'
import { MAX_TICKET_CONTENT_LEN, TicketStatusBadge } from './tickets'
import { useI18n } from '../i18n/i18n-context'

const REPLY_ROWS = 4
const TICKET_ID_PATTERN = /^[1-9]\d{0,17}$/
const STATUS_CLOSED = 'closed'

/** 一条消息：内容是纯文本，交给 React 转义后按原样（保留换行）显示（Ruling-25）。客服一律显示“客服”，不显示管理员用户名。 */
function MessageItem({ message }: { message: TicketMessage }) {
  const { t, tag } = useI18n()
  const isStaff = message.authorKind === 'admin'
  return (
    <div className={`hub-chat-item${isStaff ? ' is-staff' : ''}`}>
      <div className="hub-chat-meta">
        <strong>{t(isStaff ? 'tickets.staff' : 'tickets.me')}</strong>
        <span>{formatTime(message.createdAt, tag)}</span>
      </div>
      <div className="hub-chat-content">{message.content}</div>
    </div>
  )
}

/** 工单对话页（docs/03 §5.10）：回复后状态回到“等待回复”，回复已关闭的工单即重新打开；别人的工单后端返回 404。 */
export function TicketDetailPage() {
  const { id = '' } = useParams()
  if (!TICKET_ID_PATTERN.test(id)) {
    return <NotFoundPage />
  }
  return <TicketConversation key={id} ticketId={Number(id)} />
}

function TicketConversation({ ticketId }: { ticketId: number }) {
  const { t, tag } = useI18n()
  const toast = useToast()
  const load = useCallback((signal: AbortSignal) => api.GET('/api/user/tickets/{id}', { params: { path: { id: ticketId } }, signal }), [ticketId])
  const ticket = useRemote<TicketDetail>(load)
  const [content, setContent] = useState('')
  const [isReplying, setIsReplying] = useState(false)
  const [isConfirming, setIsConfirming] = useState(false)
  const [isClosing, setIsClosing] = useState(false)
  const detail = ticket.data
  const path = { id: ticketId }

  const handleReply = async () => {
    setIsReplying(true)
    const err = await mutate(() => api.POST('/api/user/tickets/{id}/messages', { params: { ...csrfHeader(), path }, body: { content: content.trim() } }))
    setIsReplying(false)
    if (err !== null) {
      toast.show(err.message, 'error')
      return
    }
    setContent('')
    ticket.reload()
  }

  const handleClose = async () => {
    setIsClosing(true)
    const err = await mutate(() => api.POST('/api/user/tickets/{id}/close', { params: { ...csrfHeader(), path } }))
    setIsClosing(false)
    setIsConfirming(false)
    if (err !== null) {
      toast.show(err.message, 'error')
      return
    }
    toast.show(t('tickets.closed'), 'success')
    ticket.reload()
  }

  if (detail === null) {
    return (
      <>
        <PageHeader title={t('tickets.title')} extra={<Link to={ROUTES.TICKETS}>{t('tickets.backToList')}</Link>} />
        {ticket.error === null ? <Loading isInline /> : <Alert tone="plain">{ticket.error.message}</Alert>}
      </>
    )
  }
  const isClosed = detail.status === STATUS_CLOSED
  return (
    <>
      <PageHeader title={detail.subject} extra={<Link to={ROUTES.TICKETS}>{t('tickets.backToList')}</Link>} />
      <Card
        title={t('tickets.createdAt', { time: formatTime(detail.createdAt, tag) })}
        extra={
          <span className="hub-actions">
            <TicketStatusBadge status={detail.status} />
            {!isClosed && (
              <Button variant="outline" onClick={() => setIsConfirming(true)}>
                {t('tickets.close')}
              </Button>
            )}
          </span>
        }
      >
        {ticket.error !== null && <Alert tone="plain">{ticket.error.message}</Alert>}
        {detail.messages.map((m) => (
          <MessageItem key={m.id} message={m} />
        ))}
        <TextAreaField
          label={t(isClosed ? 'tickets.replyReopen' : 'tickets.reply')}
          rows={REPLY_ROWS}
          maxLength={MAX_TICKET_CONTENT_LEN}
          value={content}
          onChange={(e) => setContent(e.target.value)}
        />
        <Button variant="primary" isBusy={isReplying} disabled={content.trim() === ''} onClick={() => void handleReply()}>
          {t('tickets.send')}
        </Button>
      </Card>
      <Confirm title={t('tickets.close')} isOpen={isConfirming} isBusy={isClosing} confirmText={t('tickets.closeConfirm')} onConfirm={() => void handleClose()} onClose={() => setIsConfirming(false)}>
        {t('tickets.closeConfirmBody')}
      </Confirm>
    </>
  )
}
