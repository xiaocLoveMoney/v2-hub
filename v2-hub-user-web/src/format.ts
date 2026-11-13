/** 展示用的格式化函数（纯函数，不依赖 DOM）。 */

const BYTES_PER_KB = 1024
const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB'] as const
const MS_PER_DAY = 86_400_000
const REGION_CODE = /^[A-Za-z]{2}$/
/** 区域指示符 A 的码点与字母 A 的差值，用来把两位地区码拼成国旗 emoji。 */
const REGIONAL_INDICATOR_OFFSET = 0x1f1e6 - 'A'.charCodeAt(0)
const PERCENT_FULL = 100

/** 把字节数格式化为带单位的字符串（1024 进制，保留两位小数）。 */
export function formatBytes(bytes: number): string {
  let value = Math.max(bytes, 0)
  let unit = 0
  while (value >= BYTES_PER_KB && unit < BYTE_UNITS.length - 1) {
    value /= BYTES_PER_KB
    unit += 1
  }
  return `${unit === 0 ? value : value.toFixed(2)} ${BYTE_UNITS[unit]}`
}

/** 本地时间的 yyyy/M/d HH:mm:ss；空值显示破折号。 */
export function formatTime(iso: string | null | undefined): string {
  if (iso === null || iso === undefined || iso === '') {
    return '—'
  }
  return new Date(iso).toLocaleString('zh-CN', { hour12: false })
}

/** 只要日期部分。 */
export function formatDate(iso: string): string {
  return new Date(iso).toLocaleDateString('zh-CN')
}

/** 距到期还有几天（向上取整，已过期为 0）；now 由调用方传入便于测试。 */
export function daysLeft(expiresAt: string, now: number): number {
  return Math.max(Math.ceil((new Date(expiresAt).getTime() - now) / MS_PER_DAY), 0)
}

/** 已用占额度的百分比（0–100，额度为 0 表示不限，返回 0）。 */
export function usedPercent(used: number, limit: number): number {
  if (limit <= 0) {
    return 0
  }
  return Math.min(Math.round((used / limit) * PERCENT_FULL), PERCENT_FULL)
}

/** 两位地区码转国旗 emoji；不是两位字母时返回空串。 */
export function regionFlag(region: string): string {
  if (!REGION_CODE.test(region)) {
    return ''
  }
  return [...region.toUpperCase()].map((ch) => String.fromCodePoint(ch.charCodeAt(0) + REGIONAL_INDICATOR_OFFSET)).join('')
}
