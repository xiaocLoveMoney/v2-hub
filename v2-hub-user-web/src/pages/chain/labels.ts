import type { ChainBlockedReason, ChainEntryState, ChainExitKind, ChainKind, ChainRoute, ChainRouteEntry } from '../../api/client'
import type { MessageKey, Translate } from '../../i18n/i18n-context'

/** 名称上限，与 hub-api.yaml 的 ChainRouteInput / ChainExitInput 一致。 */
export const MAX_CHAIN_NAME_LEN = 64
/** 分享链接上限，与 hub-api.yaml 的 ChainExitInput.link 一致。 */
export const MAX_EXIT_LINK_LEN = 2048

/** 服务端链式（docs/03 §5.19）；另一种是客户端链式 'client'。 */
export const KIND_SERVER: ChainKind = 'server'

export type BadgeTone = 'success' | 'danger' | 'warning' | 'info' | 'light'

/** 阻断原因的词条（docs/03 §5.18、§5.19）。 */
const BLOCKED_REASON_KEYS: Record<Exclude<ChainBlockedReason, ''>, MessageKey> = {
  plan: 'chain.reasonPlan',
  exit: 'chain.reasonExit',
  unsafe: 'chain.reasonUnsafe',
  quarantined: 'chain.reasonQuarantined',
}

/** 落地种类的词条（docs/03 §5.21）。 */
export const EXIT_KIND_KEYS: Record<ChainExitKind, MessageKey> = {
  platform: 'chain.exitPlatform',
  own: 'chain.exitOwn',
  node: 'chain.exitNode',
}

const STATUS_OK: ChainRoute['status'] = 'ok'
// eslint-disable-next-line no-control-regex -- 就是要拒绝控制字符
const CONTROL_CHAR_PATTERN = /[\u0000-\u001f\u007f]/

/** 规则的状态显示（docs/03 §5.18、§5.19）：阻断（带原因）优先，其次已停用，否则通畅。 */
export function routeState(t: Translate, route: Pick<ChainRoute, 'status' | 'blockedReason' | 'isEnabled'>): { label: string; tone: BadgeTone } {
  if (route.status !== STATUS_OK) {
    const reason = route.blockedReason
    const label = reason === '' ? t('chain.blocked') : t('chain.blockedWithReason', { reason: t(BLOCKED_REASON_KEYS[reason]) })
    return { label, tone: 'danger' }
  }
  if (!route.isEnabled) {
    return { label: t('chain.disabled'), tone: 'light' }
  }
  return { label: t('chain.ok'), tone: 'success' }
}

/** 起飞节点各状态的词条与颜色；inactive（规则阻断或停用）不单独标注，由规则的状态说明。 */
const ENTRY_STATE_META: Record<Exclude<ChainEntryState, 'inactive'>, { labelKey: MessageKey; tone: BadgeTone }> = {
  ok: { labelKey: 'chain.entryOk', tone: 'success' },
  applied: { labelKey: 'chain.entryApplied', tone: 'success' },
  pending: { labelKey: 'chain.entryPending', tone: 'info' },
  failed: { labelKey: 'chain.entryFailed', tone: 'danger' },
  shadowed: { labelKey: 'chain.entryShadowed', tone: 'light' },
  duplicate: { labelKey: 'chain.entryDuplicate', tone: 'light' },
  unavailable: { labelKey: 'chain.entryUnavailable', tone: 'warning' },
}

/** 一个起飞节点的状态显示；规则阻断或停用时返回 null（不标注）。 */
export function entryState(t: Translate, entry: Pick<ChainRouteEntry, 'state' | 'coveredBy'>): { label: string; tone: BadgeTone } | null {
  if (entry.state === 'inactive') {
    return null
  }
  const meta = ENTRY_STATE_META[entry.state]
  return { label: t(meta.labelKey, { n: entry.coveredBy }), tone: meta.tone }
}

/** 名称是否合法：不超过上限、不含换行等控制字符（与后端校验一致）。 */
export function isValidChainName(name: string): boolean {
  // 按字符计数，与后端按 rune 计数一致
  const length = [...name].length
  return length <= MAX_CHAIN_NAME_LEN && !CONTROL_CHAR_PATTERN.test(name)
}
