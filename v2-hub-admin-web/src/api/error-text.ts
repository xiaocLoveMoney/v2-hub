import { formatBytes } from '../bytes'

// 错误信封里需要结合 data 才能说清楚的提示（docs/03 §5.23）；由 client.ts 的 toApiError 统一调用，调用点不用再包一层

/** 代理的流量池不够本次开通，信封 data 带 poolCost / poolRemaining（docs/03 §5.23）。 */
export const CODE_AGENT_POOL_INSUFFICIENT = 'agent_pool_insufficient'
const POOL_INSUFFICIENT_FALLBACK = '流量池不够本次开通，请联系平台补充'

/**
 * 流量池不够时的提示文字（开通前的预判与后端拒绝共用）。
 * @param cost 本次开通要扣减的字节数
 * @param remaining 池里剩余的字节数
 */
export function poolInsufficientText(cost: number, remaining: number): string {
  return `本次需要 ${formatBytes(cost)}，流量池只剩 ${formatBytes(remaining)}，请联系平台补充`
}

/** 从错误信封的 data 里取一个数值字段；取不到返回 null。 */
function numberField(data: unknown, key: string): number | null {
  if (typeof data !== 'object' || data === null || !(key in data)) {
    return null
  }
  const value = (data as Record<string, unknown>)[key]
  return typeof value === 'number' ? value : null
}

/**
 * 给人看的错误文案：需要 data 才能说清楚的错误码换成具体提示，其余原样用后端的 message。
 * @param code 错误码
 * @param message 后端给的 message
 * @param data 信封里的 data（可能没有）
 */
export function describeErrorMessage(code: string, message: string, data: unknown): string {
  if (code !== CODE_AGENT_POOL_INSUFFICIENT) {
    return message
  }
  const cost = numberField(data, 'poolCost')
  const remaining = numberField(data, 'poolRemaining')
  return cost !== null && remaining !== null ? poolInsufficientText(cost, remaining) : POOL_INSUFFICIENT_FALLBACK
}
