import dayjs from 'dayjs'
import type { LatencyPoint } from '../../../api/client'
import { centsToAmount } from '../../../money'
import { BYTES_PER_GB } from '../../users/quota'

/** 服务器状态卡片的格式化与阈值（docs/05 服务器状态卡片，Ruling-83 – Ruling-86）。 */

const BYTE_STEP = 1024
const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'] as const
const SECONDS_PER_DAY = 86400
const SECONDS_PER_HOUR = 3600
const MS_PER_DAY = 86400000
export const PERCENT_FULL = 100
/** 接口里日期（到期日、周期起止）的格式。 */
export const DATE_FORMAT = 'YYYY-MM-DD'
/** 三位有效数字：小于 10 的保留两位小数，小于 100 的保留一位，其余取整。 */
const DECIMALS_SMALL = 2
const DECIMALS_MEDIUM = 1
const SMALL_LIMIT = 10
const MEDIUM_LIMIT = 100

/** 用量阈值（百分比）：到 WARN 变橙、到 DANGER 变红。 */
const USAGE_WARN_PERCENT = 70
const USAGE_DANGER_PERCENT = 90
/** 延迟阈值（毫秒）与丢包阈值（百分比）。 */
const LATENCY_WARN_MS = 200
const LATENCY_DANGER_MS = 300
const LOSS_WARN_PERCENT = 1
const LOSS_DANGER_PERCENT = 10

/** 一格或一个数值的状态：正常、偏高、过高、没有数据。 */
export type Level = 'ok' | 'warn' | 'danger' | 'empty'

function fixed(value: number): string {
  if (value < SMALL_LIMIT) {
    return value.toFixed(DECIMALS_SMALL)
  }
  if (value < MEDIUM_LIMIT) {
    return value.toFixed(DECIMALS_MEDIUM)
  }
  return value.toFixed(0)
}

/** 字节数按 1024 进位，取三位有效数字，如 309 MB、1.00 TB。 */
export function formatBytes(bytes: number): string {
  let value = Math.max(bytes, 0)
  let unit = 0
  while (value >= BYTE_STEP && unit < BYTE_UNITS.length - 1) {
    value /= BYTE_STEP
    unit += 1
  }
  return unit === 0 ? `${value.toFixed(0)} B` : `${fixed(value)} ${BYTE_UNITS[unit]}`
}

/** 速率（字节每秒），如 29.0 KB/s。 */
export function formatRate(bytesPerSecond: number): string {
  return `${formatBytes(bytesPerSecond)}/s`
}

/** 百分比，保留一位小数；总量为 0 时为 0。 */
export function percentOf(used: number, total: number): number {
  if (total <= 0) {
    return 0
  }
  return Math.min((used / total) * PERCENT_FULL, PERCENT_FULL)
}

/** 百分比文字，如 32.0%。 */
export function formatPercent(value: number): string {
  return `${value.toFixed(DECIMALS_MEDIUM)}%`
}

/** 用量百分比对应的状态。 */
export function usageLevel(percent: number): Level {
  if (percent >= USAGE_DANGER_PERCENT) {
    return 'danger'
  }
  return percent >= USAGE_WARN_PERCENT ? 'warn' : 'ok'
}

/** 一次延迟检测对应的状态：全丢或延迟过高为红，偏高或有丢包为橙。 */
export function latencyLevel(point: LatencyPoint | undefined): Level {
  if (point === undefined) {
    return 'empty'
  }
  if (point.avgMs === null || point.avgMs >= LATENCY_DANGER_MS || point.lossPercent >= LOSS_DANGER_PERCENT) {
    return 'danger'
  }
  return point.avgMs >= LATENCY_WARN_MS || point.lossPercent >= LOSS_WARN_PERCENT ? 'warn' : 'ok'
}

/** 丢包率对应的状态。 */
export function lossLevel(point: LatencyPoint | undefined): Level {
  if (point === undefined) {
    return 'empty'
  }
  if (point.lossPercent >= LOSS_DANGER_PERCENT) {
    return 'danger'
  }
  return point.lossPercent >= LOSS_WARN_PERCENT ? 'warn' : 'ok'
}

/** 开机时长：满一天按天，不满按小时。 */
export function formatUptime(seconds: number): { value: string; unit: string } {
  if (seconds >= SECONDS_PER_DAY) {
    return { value: String(Math.floor(seconds / SECONDS_PER_DAY)), unit: '天' }
  }
  return { value: String(Math.floor(seconds / SECONDS_PER_HOUR)), unit: '小时' }
}

/** 两个日期（YYYY-MM-DD）相差的天数；date 在 today 之前为负。 */
export function daysBetween(today: string, date: string): number {
  return Math.round((Date.parse(date) - Date.parse(today)) / MS_PER_DAY)
}

/** 当前时刻在本地时区的日期（YYYY-MM-DD）。 */
export function localToday(now: number): string {
  return dayjs(now).format(DATE_FORMAT)
}

/** 计费周期的显示名。 */
export const BILLING_CYCLE_LABELS: Record<string, string> = {
  month: '月',
  quarter: '季',
  half_year: '半年',
  year: '年',
  once: '一次性',
}

/** 流量计量方式的显示名。 */
export const TRAFFIC_MODE_LABELS: Record<string, string> = {
  both: '收发合计',
  out: '只算出站',
  max: '收发取大',
}

/** 价格，如 $49.90/年；币种不被浏览器认识时按“49.90 XXX”。 */
export function formatPrice(cents: number, currency: string, cycle: string): string {
  const amount = centsToAmount(cents)
  let text: string
  try {
    text = new Intl.NumberFormat('zh-CN', { style: 'currency', currency }).format(amount)
  } catch (err) {
    reportError(err)
    text = `${amount.toFixed(DECIMALS_SMALL)} ${currency}`
  }
  const cycleLabel = BILLING_CYCLE_LABELS[cycle] ?? cycle
  return cycle === 'once' ? `${text}（${cycleLabel}）` : `${text}/${cycleLabel}`
}

/** 字节换算成 GB（1024 进位），给表单用。 */
export function bytesToGiB(bytes: number): number {
  return bytes / BYTES_PER_GB
}
