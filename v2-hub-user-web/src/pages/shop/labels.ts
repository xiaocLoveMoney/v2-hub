import type { LedgerKind, Order, ShopPlan } from '../../api/client'
import { formatBytes } from '../../format'
import { plural, type MessageKey, type Translate } from '../../i18n/i18n-context'

/** 商店、钱包与落地页里反复用到的显示文案（纯函数，翻译函数由调用方传入，便于测试）。 */

const DAYS_PER_MONTH = 30
const DAYS_PER_YEAR = 365
const MONTHS_PER_YEAR = 12
const UNLIMITED = 0

/** 时长选项的显示名：整月 / 整年按月、年说，其余按天。 */
export function durationLabel(t: Translate, days: number): string {
  if (days % DAYS_PER_YEAR === 0) {
    return plural(t, days / DAYS_PER_YEAR, 'shop.durationYear', 'shop.durationYears')
  }
  if (days % DAYS_PER_MONTH === 0 && days / DAYS_PER_MONTH < MONTHS_PER_YEAR) {
    return plural(t, days / DAYS_PER_MONTH, 'shop.durationMonth', 'shop.durationMonths')
  }
  return plural(t, days, 'shop.durationDay', 'shop.durationDays')
}

/** 周期流量重置对应的说法；取值见 hub-api.yaml 的 TrafficReset。 */
const RESET_KEYS: Record<NonNullable<ShopPlan['trafficReset']>, MessageKey> = {
  never: 'shop.trafficTotal',
  hourly: 'shop.trafficHourly',
  daily: 'shop.trafficDaily',
  weekly: 'shop.trafficWeekly',
  monthly: 'shop.trafficMonthly',
}

/** 套餐流量的说法：按周期重置的写“每月 X”，不重置的写“有效期内共 X”（docs/03 §5.12 时长选项）。 */
export function trafficLabel(t: Translate, limit: number, reset: ShopPlan['trafficReset']): string {
  if (limit === UNLIMITED) {
    return t('shop.trafficUnlimited')
  }
  return t(RESET_KEYS[reset ?? 'never'] ?? RESET_KEYS.never, { amount: formatBytes(limit) })
}

/** 设备数的说法。 */
export function deviceLabel(t: Translate, limitIp: number): string {
  return limitIp === UNLIMITED ? t('shop.devicesUnlimited') : plural(t, limitIp, 'shop.devicesOne', 'shop.devicesMany')
}

/** 流水类型的显示名词条；取值见 hub-api.yaml 的 LedgerKind。 */
export const LEDGER_KIND_KEYS: Record<LedgerKind, MessageKey> = {
  topup_code: 'ledger.topupCode',
  topup_online: 'ledger.topupOnline',
  purchase: 'ledger.purchase',
  renew: 'ledger.renew',
  rebate: 'ledger.rebate',
  admin_adjust: 'ledger.adminAdjust',
  forfeit: 'ledger.forfeit',
}

/** 订单类型的显示名；试用单独标出。 */
export function orderKindLabel(t: Translate, order: Pick<Order, 'kind' | 'isTrial'>): string {
  if (order.isTrial) {
    return t('order.trial')
  }
  switch (order.kind) {
    case 'traffic_pack':
      return t('order.trafficPack')
    case 'renew':
      return t('order.renew')
    case 'plan':
      return t('order.plan')
    default:
      return order.kind
  }
}

/** 价格卡的卖点行：流量、设备数、节点数，再加运营写的卖点（一行一条）；商店与落地页共用。 */
export function planFeatureLines(t: Translate, plan: Pick<ShopPlan, 'trafficLimit' | 'trafficReset' | 'limitIp' | 'nodeCount' | 'description'>): string[] {
  const extras = plan.description.split('\n').filter((line) => line.trim() !== '')
  return [trafficLabel(t, plan.trafficLimit, plan.trafficReset), deviceLabel(t, plan.limitIp), plural(t, plan.nodeCount, 'shop.nodesOne', 'shop.nodesMany'), ...extras]
}
