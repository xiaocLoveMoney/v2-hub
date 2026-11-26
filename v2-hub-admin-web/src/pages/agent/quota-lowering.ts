import type { AgentUser, AgentUserQuotaInput } from '../../api/client'
import { formatTime } from '../system/format'
import { formatBytes } from '../users/quota'

// 代理调低额度 / 提前到期的前端预检，照 docs/03 §5.23“调低的判定”（Ruling-103）；后端仍是最终裁决

/** 表单上改了的两项；null = 这一项没动。 */
export interface QuotaLoweringDraft {
  /** 新额度（字节）。 */
  trafficLimit: number | null
  /** 新到期时间（ISO）。 */
  expiresAt: string | null
}

/** 预检结果：通过时 body 是要提交的内容（只含真正变了的字段），否则 error 写明原因。 */
export type QuotaLoweringResult = { body: AgentUserQuotaInput; error: null } | { body: null; error: string }

const NOTHING_CHANGED = '没有改动：请调低流量额度或提前到期时间'

/**
 * 按“调低的判定”检查一次：额度须大于 0 且不高于当前（当前不限时任何正数都算调低）；
 * 到期时间不晚于当前（当前不过期时任何时间都算提前，可以是过去）；等待首次使用时不能改到期时间；与当前值相等视为不变。
 * @param user 名下用户的当前值
 * @param draft 表单上改了的项
 * @returns 要提交的内容或错误原因
 */
export function checkQuotaLowering(user: Pick<AgentUser, 'trafficLimit' | 'expiresAt' | 'pendingDays'>, draft: QuotaLoweringDraft): QuotaLoweringResult {
  const body: AgentUserQuotaInput = {}
  if (draft.trafficLimit !== null) {
    if (draft.trafficLimit <= 0) {
      return { body: null, error: '流量额度须大于 0，不能设为不限' }
    }
    const isUnlimited = user.trafficLimit === 0
    if (!isUnlimited && draft.trafficLimit > user.trafficLimit) {
      return { body: null, error: `只能调低：新额度不能高于当前的 ${formatBytes(user.trafficLimit)}` }
    }
    if (draft.trafficLimit !== user.trafficLimit) {
      body.trafficLimit = draft.trafficLimit
    }
  }
  if (draft.expiresAt !== null) {
    const current = user.expiresAt ?? null
    if (current === null && user.pendingDays > 0) {
      return { body: null, error: '用户还在等首次使用、尚未开始计时，不能改到期时间' }
    }
    const next = Date.parse(draft.expiresAt)
    if (current !== null && next > Date.parse(current)) {
      return { body: null, error: `只能提前：新到期时间不能晚于当前的 ${formatTime(current)}` }
    }
    if (current === null || next !== Date.parse(current)) {
      body.expiresAt = draft.expiresAt
    }
  }
  if (body.trafficLimit === undefined && body.expiresAt === undefined) {
    return { body: null, error: NOTHING_CHANGED }
  }
  return { body, error: null }
}
