import { Tag, Typography, type TableProps } from 'antd'
import type { LedgerEntry, LedgerKind, Order, OrderKind } from '../../api/client'
import { formatMoney } from '../../money'
import { formatTime } from '../system/format'
import { formatBytes } from '../users/quota'

// 订单与余额流水表的列与筛选项：员工的交易记录页与代理的记录页共用（docs/03 §5.13、§5.23）。金额只做显示格式化。

const UNLIMITED = 0

/** 订单类型的显示名；取值见 hub-api.yaml 的 OrderKind。 */
const ORDER_KIND_LABELS: Record<OrderKind, string> = { plan: '套餐', traffic_pack: '流量包', renew: '自动续费' }
/** 流水类型的显示名；取值见 hub-api.yaml 的 LedgerKind。 */
const LEDGER_KIND_LABELS: Record<LedgerKind, string> = {
  topup_code: '充值码', topup_online: '在线充值', purchase: '购买', renew: '自动续费', rebate: '邀请返利', admin_adjust: '管理员调整', forfeit: '注销作废',
}
function optionsOf<K extends string>(labels: Record<K, string>): { value: K; label: string }[] {
  return (Object.keys(labels) as K[]).map((value) => ({ value, label: labels[value] }))
}

/** 用户已删除时没有 userId，只显示下单时留下的用户名快照。 */
export function userCell(username: string | undefined, userId: number | null | undefined): string {
  const name = username ?? ''
  return userId === null || userId === undefined ? `${name}（已删除）` : `${name} #${userId}`
}

/** 订单类型的筛选项。 */
export const ORDER_KIND_OPTIONS = optionsOf(ORDER_KIND_LABELS)
/** 流水类型的筛选项。 */
export const LEDGER_KIND_OPTIONS = optionsOf(LEDGER_KIND_LABELS)

/**
 * 订单表的列（员工与代理的订单页共用）。
 * @param currency 显示币种
 */
export function orderColumns(currency: string): TableProps<Order>['columns'] {
  return [
    { title: '时间', dataIndex: 'createdAt', render: (v: string) => formatTime(v) },
    { title: '订单号', dataIndex: 'orderNo', render: (v: string) => <span className="mono">{v}</span> },
    { title: '用户', key: 'user', render: (_: unknown, o: Order) => userCell(o.username, o.userId) },
    { title: '类型', dataIndex: 'kind', render: (k: OrderKind, o: Order) => <Tag color={o.isTrial ? 'gold' : undefined}>{o.isTrial ? '试用' : (ORDER_KIND_LABELS[k] ?? k)}</Tag> },
    {
      title: '商品',
      key: 'item',
      render: (_: unknown, o: Order) =>
        o.kind === 'traffic_pack' ? `${o.itemName}（${formatBytes(o.trafficBytes)}）` : `${o.itemName}（${o.durationDays} 天 / ${o.trafficBytes === UNLIMITED ? '不限流量' : formatBytes(o.trafficBytes)}）`,
    },
    { title: '原价', dataIndex: 'amountOriginal', render: (v: number) => formatMoney(v, currency) },
    { title: '优惠', key: 'discount', render: (_: unknown, o: Order) => (o.amountDiscount === 0 ? '—' : `${formatMoney(o.amountDiscount, currency)}（${o.couponCode}）`) },
    { title: '实付', dataIndex: 'amountPaid', render: (v: number) => <Typography.Text strong>{formatMoney(v, currency)}</Typography.Text> },
  ]
}

/**
 * 余额流水表的列（员工与代理的流水页共用）。
 * @param currency 显示币种
 * @param hasOperator 是否显示操作人列；代理侧接口不带员工账号名（docs/03 §5.23），不显示
 */
export function ledgerColumns(currency: string, hasOperator: boolean): TableProps<LedgerEntry>['columns'] {
  const columns: TableProps<LedgerEntry>['columns'] = [
    { title: '时间', dataIndex: 'createdAt', render: (v: string) => formatTime(v) },
    { title: '用户', key: 'user', render: (_: unknown, l: LedgerEntry) => userCell(l.username, l.userId) },
    { title: '类型', dataIndex: 'kind', render: (k: LedgerKind) => <Tag>{LEDGER_KIND_LABELS[k] ?? k}</Tag> },
    {
      title: '变动',
      dataIndex: 'amount',
      render: (v: number) => <Typography.Text type={v < 0 ? 'danger' : 'success'}>{`${v > 0 ? '+' : ''}${formatMoney(v, currency)}`}</Typography.Text>,
    },
    { title: '变动后余额', dataIndex: 'balanceAfter', render: (v: number) => formatMoney(v, currency) },
  ]
  if (hasOperator) {
    columns.push({ title: '操作人', dataIndex: 'adminUsername', render: (v: string | undefined) => v || '—' })
  }
  columns.push({ title: '备注', dataIndex: 'remark', render: (v: string) => v || '—' })
  return columns
}
