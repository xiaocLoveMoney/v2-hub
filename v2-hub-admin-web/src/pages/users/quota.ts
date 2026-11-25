import type { TrafficReset, UserStatus } from '../../api/client'
import { formatTime } from '../system/format'

/** 1 GB 的字节数；额度在界面上按 GB 填写，接口按字节传（hub-api PlanInput.trafficLimit）。 */
export const BYTES_PER_GB = 1024 ** 3
const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB'] as const
const BYTE_STEP = 1024
const DECIMALS = 2

/** 字节数格式化为人读的大小。 */
export function formatBytes(bytes: number): string {
  let value = bytes
  let unit = 0
  while (value >= BYTE_STEP && unit < BYTE_UNITS.length - 1) {
    value /= BYTE_STEP
    unit++
  }
  return `${unit === 0 ? value : value.toFixed(DECIMALS)} ${BYTE_UNITS[unit]}`
}

export function gbToBytes(gb: number | null | undefined): number {
  return Math.round((gb ?? 0) * BYTES_PER_GB)
}

export function bytesToGb(bytes: number): number {
  return Number((bytes / BYTES_PER_GB).toFixed(DECIMALS))
}

/** 用户状态（docs/03 §2）的标签与颜色。 */
export const USER_STATUS: Record<UserStatus, { label: string; color: string }> = {
  active: { label: '正常', color: 'green' },
  limited: { label: '流量用尽', color: 'orange' },
  expired: { label: '已到期', color: 'red' },
  disabled: { label: '已停用', color: 'default' },
}

export const TRAFFIC_RESET_OPTIONS: { value: TrafficReset; label: string }[] = [
  { value: 'never', label: '不重置' },
  { value: 'hourly', label: '每小时' },
  { value: 'daily', label: '每天' },
  { value: 'weekly', label: '每周（周日 0 点）' },
  { value: 'monthly', label: '每月' },
]

export const WEEKDAY_OPTIONS = ['周一', '周二', '周三', '周四', '周五', '周六', '周日'].map((label, i) => ({ value: i + 1, label }))

/** 自动续期三选一（照 3x-ui）：界面上用一个模式字段 + 一个数值表达。 */
export type RenewMode = 'none' | 'days' | 'month' | 'week'

export interface RenewFields {
  renewEveryDays: number
  renewMonthDay: number
  renewWeekday: number
}

export function renewModeOf(v: RenewFields): RenewMode {
  if (v.renewMonthDay > 0) {
    return 'month'
  }
  if (v.renewWeekday > 0) {
    return 'week'
  }
  return v.renewEveryDays > 0 ? 'days' : 'none'
}

export function renewValueOf(v: RenewFields): number {
  return v.renewMonthDay || v.renewWeekday || v.renewEveryDays || 1
}

/** 把模式 + 数值还原成接口的三个字段，未选中的方式为 0。 */
export function renewFieldsOf(mode: RenewMode, value: number | null | undefined): RenewFields {
  const n = value ?? 0
  return { renewEveryDays: mode === 'days' ? n : 0, renewMonthDay: mode === 'month' ? n : 0, renewWeekday: mode === 'week' ? n : 0 }
}

/** 自动续期的文字说明。 */
export function describeRenew(v: RenewFields & { renewMax: number }): string {
  const max = v.renewMax > 0 ? `，最多 ${v.renewMax} 次` : ''
  switch (renewModeOf(v)) {
    case 'days':
      return `每 ${v.renewEveryDays} 天${max}`
    case 'month':
      return `每月 ${v.renewMonthDay} 号${max}`
    case 'week':
      return `每${WEEKDAY_OPTIONS[v.renewWeekday - 1]?.label ?? ''}${max}`
    case 'none':
    default:
      return '不续期'
  }
}

export function describeReset(reset: TrafficReset, day: number): string {
  if (reset === 'monthly') {
    return `每月 ${day} 号`
  }
  return TRAFFIC_RESET_OPTIONS.find((o) => o.value === reset)?.label ?? reset
}

/** 到期时间的文字：有到期时间显示时间，等待首次使用显示天数，否则不过期。 */
export function describeExpiry(u: { expiresAt?: string | null; pendingDays: number }): string {
  if (u.expiresAt !== null && u.expiresAt !== undefined) {
    return formatTime(u.expiresAt)
  }
  return u.pendingDays > 0 ? `首次使用后 ${u.pendingDays} 天` : '不过期'
}
