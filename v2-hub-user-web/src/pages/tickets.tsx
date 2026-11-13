import type { TicketStatus } from '../api/client'
import { Badge } from '../components/ui'

/** 工单内容上限，与 hub-api.yaml 的 TicketCreateInput / TicketMessageInput 一致。 */
export const MAX_TICKET_SUBJECT_LEN = 100
export const MAX_TICKET_CONTENT_LEN = 5000

/** 用户视角的状态名（docs/03 §5.10）：open = 等客服回复，answered = 客服已回复。 */
const STATUS_META: Record<TicketStatus, { label: string; tone?: 'warning' | 'success' }> = {
  open: { label: '等待回复', tone: 'warning' },
  answered: { label: '已回复', tone: 'success' },
  closed: { label: '已关闭' },
}

/** 工单状态徽章。 */
export function TicketStatusBadge({ status }: { status: TicketStatus }) {
  const meta = STATUS_META[status]
  return <Badge tone={meta?.tone}>{meta?.label ?? status}</Badge>
}
