import { useCallback, useState } from 'react'
import { Link, generatePath, useNavigate } from 'react-router'
import { api, csrfHeader, toApiError, type PageData, type Ticket } from '../api/client'
import { Alert, Button, Card, Empty, Field, Modal, PageHeader, Pager, TextAreaField } from '../components/ui'
import { formatTime } from '../format'
import { useRemote } from '../hooks/use-remote'
import { ROUTES } from '../menu'
import { MAX_TICKET_CONTENT_LEN, MAX_TICKET_SUBJECT_LEN, TicketStatusBadge } from './tickets'

const PAGE_SIZE = 20
const CONTENT_ROWS = 6

/** 工单支持（docs/03 §5.10）：本人的工单列表与新建；工单关闭时菜单不出现，接口也会拒绝。 */
export function TicketsPage() {
  const [page, setPage] = useState(1)
  const [isCreating, setIsCreating] = useState(false)
  const load = useCallback((signal: AbortSignal) => api.GET('/api/user/tickets', { params: { query: { page, pageSize: PAGE_SIZE } }, signal }), [page])
  const tickets = useRemote<PageData<Ticket>>(load)
  const items = tickets.data?.items ?? []

  return (
    <>
      <PageHeader
        title="工单支持"
        extra={
          <Button variant="primary" onClick={() => setIsCreating(true)}>
            新建工单
          </Button>
        }
      />
      <Card>
        {tickets.error !== null && <Alert tone="plain">{tickets.error.message}</Alert>}
        <div className="hub-table-wrap">
          <table className="hub-table">
            <thead>
              <tr>
                <th>主题</th>
                <th>状态</th>
                <th>最近回复</th>
                <th>创建时间</th>
              </tr>
            </thead>
            <tbody>
              {items.map((t) => (
                <tr key={t.id}>
                  <td>
                    <Link to={generatePath(ROUTES.TICKET_DETAIL, { id: String(t.id) })}>{t.subject}</Link>
                  </td>
                  <td>
                    <TicketStatusBadge status={t.status} />
                  </td>
                  <td>{formatTime(t.lastReplyAt)}</td>
                  <td>{formatTime(t.createdAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {tickets.data !== null && items.length === 0 && <Empty>还没有工单，遇到问题可以点右上角新建</Empty>}
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
      title="新建工单"
      isOpen
      onClose={onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button variant="primary" isBusy={isBusy} disabled={subject.trim() === '' || content.trim() === ''} onClick={() => void handleCreate()}>
            提交
          </Button>
        </>
      }
    >
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <Field label="主题" maxLength={MAX_TICKET_SUBJECT_LEN} value={subject} onChange={(e) => setSubject(e.target.value)} />
      <TextAreaField
        label="问题描述"
        rows={CONTENT_ROWS}
        maxLength={MAX_TICKET_CONTENT_LEN}
        hint="纯文本；请不要把订阅地址、口令等凭据写进来"
        value={content}
        onChange={(e) => setContent(e.target.value)}
      />
    </Modal>
  )
}
