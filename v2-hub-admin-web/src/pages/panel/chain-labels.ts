import type { AdminServerChainRule, ChainApplyStatus, ChainBlockedReason, ServerChainApplyState } from '../../api/client'

/** 入口服务器下发状态的显示名与颜色（docs/03 §3 C1–C5）。 */
export const CHAIN_APPLY_STATUS_META: Record<ChainApplyStatus, { label: string; color?: string }> = {
  none: { label: '无规则' },
  pending: { label: '待下发', color: 'blue' },
  applied: { label: '已生效', color: 'green' },
  failed: { label: '下发失败', color: 'red' },
}

/** 阻断原因（docs/03 §5.18、§5.19）。 */
const BLOCKED_REASON_LABELS: Record<Exclude<ChainBlockedReason, ''>, string> = {
  plan: '套餐不支持',
  entry: '入口不可用',
  exit: '落地不可用',
}

const APPLY_STATE_META: Record<Exclude<ServerChainApplyState, ''>, { label: string; color: string }> = {
  applied: { label: '生效', color: 'green' },
  pending: { label: '下发中', color: 'blue' },
  failed: { label: '下发失败', color: 'red' },
}

const STATUS_OK = 'ok'

/** 一条服务端规则的状态显示：阻断（带原因）、已停用，或下发情况。 */
export function serverRuleState(r: Pick<AdminServerChainRule, 'status' | 'blockedReason' | 'isEnabled' | 'applyStatus'>): { label: string; color?: string } {
  if (r.status !== STATUS_OK) {
    const reason = r.blockedReason === '' ? '' : ` · ${BLOCKED_REASON_LABELS[r.blockedReason]}`
    return { label: `阻断${reason}`, color: 'orange' }
  }
  if (!r.isEnabled || r.applyStatus === '') {
    return { label: '已停用' }
  }
  return APPLY_STATE_META[r.applyStatus]
}
