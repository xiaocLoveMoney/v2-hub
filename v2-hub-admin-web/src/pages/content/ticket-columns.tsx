import { Button, Tag, type TableProps } from 'antd'
import type { Ticket, TicketStatus } from '../../api/client'
import { formatTime } from '../system/format'

// 工单的状态显示与列表列：员工与代理的工单页共用（docs/03 §5.10、§5.23）

/** 工单状态的显示名与颜色；取值见 hub-api.yaml 的 TicketStatus。 */
const STATUS_META: Record<TicketStatus, { label: string; color?: string }> = {
  open: { label: '待回复', color: 'orange' },
  answered: { label: '已回复', color: 'green' },
  closed: { label: '已关闭' },
}
/** 工单状态的筛选项（员工与代理的工单页共用）。 */
export const TICKET_STATUS_OPTIONS = (Object.keys(STATUS_META) as TicketStatus[]).map((value) => ({ value, label: STATUS_META[value].label }))

/** 工单状态标签。 */
export function renderTicketStatus(status: TicketStatus) {
  return <Tag color={STATUS_META[status]?.color}>{STATUS_META[status]?.label ?? status}</Tag>
}

/**
 * 工单列表的列（员工与代理的工单页共用）。
 * @param onOpen 点“查看”时打开详情
 */
export function ticketColumns(onOpen: (id: number) => void): TableProps<Ticket>['columns'] {
  return [
    { title: '主题', dataIndex: 'subject' },
    { title: '用户', key: 'user', render: (_: unknown, t: Ticket) => `${t.username ?? ''} #${t.userId ?? ''}` },
    { title: '状态', dataIndex: 'status', render: (s: TicketStatus) => renderTicketStatus(s) },
    { title: '最近回复', dataIndex: 'lastReplyAt', render: (v: string) => formatTime(v) },
    { title: '创建时间', dataIndex: 'createdAt', render: (v: string) => formatTime(v) },
    {
      title: '操作',
      key: 'actions',
      render: (_: unknown, t: Ticket) => (
        <Button size="small" onClick={() => onOpen(t.id)}>
          查看
        </Button>
      ),
    },
  ]
}
