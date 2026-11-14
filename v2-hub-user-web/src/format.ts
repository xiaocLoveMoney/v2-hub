/** 展示用的格式化函数（纯函数，不依赖 DOM）。 */

const BYTES_PER_KB = 1024
const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB'] as const
const MS_PER_DAY = 86_400_000
const PERCENT_FULL = 100
const DEFAULT_TAG = 'zh-CN'

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

/** 本地时间（24 小时制，按界面语言标签排版）；空值显示破折号。 */
export function formatTime(iso: string | null | undefined, tag: string = DEFAULT_TAG): string {
  if (iso === null || iso === undefined || iso === '') {
    return '—'
  }
  return new Date(iso).toLocaleString(tag, { hour12: false })
}

/** 只要日期部分（按界面语言标签排版）。 */
export function formatDate(iso: string, tag: string = DEFAULT_TAG): string {
  return new Date(iso).toLocaleDateString(tag)
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

/** 国家码的本地化名称；不认识的码原样返回。 */
export function countryName(code: string, localeTag: string): string {
  try {
    return new Intl.DisplayNames([localeTag], { type: 'region', fallback: 'code' }).of(code) ?? code
  } catch (err) {
    // 非法的码会抛 RangeError：按原样显示即可
    if (err instanceof RangeError) {
      return code
    }
    throw err
  }
}
