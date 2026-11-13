import type { TicketStatus } from '../api/client'
import { Badge } from '../components/ui'
import { useI18n, type MessageKey } from '../i18n/i18n-context'

/** 工单内容上限，与 hub-api.yaml 的 TicketCreateInput / TicketMessageInput 一致。 */
export const MAX_TICKET_SUBJECT_LEN = 100
export const MAX_TICKET_CONTENT_LEN = 5000

/** 用户视角的状态名（docs/03 §5.10）：open = 等客服回复，answered = 客服已回复。 */
const STATUS_META: Record<TicketStatus, { labelKey: MessageKey; tone?: 'warning' | 'success' }> = {
  open: { labelKey: 'tickets.statusOpen', tone: 'warning' },
  answered: { labelKey: 'tickets.statusAnswered', tone: 'success' },
  closed: { labelKey: 'tickets.statusClosed' },
}

/** 工单状态徽章。 */
export function TicketStatusBadge({ status }: { status: TicketStatus }) {
  const { t } = useI18n()
  const meta = STATUS_META[status]
  return <Badge tone={meta?.tone}>{meta === undefined ? status : t(meta.labelKey)}</Badge>
}
