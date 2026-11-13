import type { LedgerKind, Order, ShopPlan } from '../../api/client'
import { formatBytes } from '../../format'

/** 商店与钱包里反复用到的显示文案（纯函数，便于测试）。 */

const DAYS_PER_MONTH = 30
const DAYS_PER_YEAR = 365
const MONTHS_PER_YEAR = 12
const UNLIMITED = 0

/** 时长选项的显示名：整月 / 整年按月、年说，其余按天。 */
export function durationLabel(days: number): string {
  if (days % DAYS_PER_YEAR === 0) {
    return `${days / DAYS_PER_YEAR} 年`
  }
  if (days % DAYS_PER_MONTH === 0 && days / DAYS_PER_MONTH < MONTHS_PER_YEAR) {
    return `${days / DAYS_PER_MONTH} 个月`
  }
  return `${days} 天`
}

/** 周期流量重置对应的说法；取值见 hub-api.yaml 的 TrafficReset。 */
const RESET_PREFIX: Record<NonNullable<ShopPlan['trafficReset']>, string> = {
  never: '有效期内共',
  hourly: '每小时',
  daily: '每天',
  weekly: '每周',
  monthly: '每月',
}

/** 套餐流量的说法：按周期重置的写“每月 X”，不重置的写“有效期内共 X”（docs/03 §5.12 时长选项）。 */
export function trafficLabel(limit: number, reset: ShopPlan['trafficReset']): string {
  if (limit === UNLIMITED) {
    return '不限流量'
  }
  return `${RESET_PREFIX[reset ?? 'never'] ?? RESET_PREFIX.never} ${formatBytes(limit)}`
}

/** 流水类型的显示名；取值见 hub-api.yaml 的 LedgerKind。 */
export const LEDGER_KIND_LABELS: Record<LedgerKind, string> = {
  topup_code: '充值码',
  topup_online: '在线充值',
  purchase: '购买',
  renew: '自动续费',
  rebate: '邀请返利',
  admin_adjust: '客服调整',
  forfeit: '注销作废',
}

/** 订单类型的显示名；试用单独标出。 */
export function orderKindLabel(order: Pick<Order, 'kind' | 'isTrial'>): string {
  if (order.isTrial) {
    return '试用'
  }
  switch (order.kind) {
    case 'traffic_pack':
      return '流量包'
    case 'renew':
      return '自动续费'
    case 'plan':
      return '套餐'
    default:
      return order.kind
  }
}
