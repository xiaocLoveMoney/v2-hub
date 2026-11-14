import type { AdminChainRoute, ChainApplyStatus, ChainBlockedReason, ChainEntryState, ChainExitKind, ChainKind } from '../../api/client'

/** 标签的显示名与颜色；color 省略 = 默认灰色。 */
export interface TagMeta {
  label: string
  color?: string
}

/** 起飞服务器下发状态的显示名与颜色（docs/03 §3 C1–C5）。 */
export const CHAIN_APPLY_STATUS_META: Record<ChainApplyStatus, TagMeta> = {
  none: { label: '无规则' },
  pending: { label: '待下发', color: 'blue' },
  applied: { label: '已生效', color: 'green' },
  failed: { label: '下发失败', color: 'red' },
}

/** 规则类型（docs/03 §5.18、§5.19）。 */
export const CHAIN_KIND_LABELS: Record<ChainKind, string> = {
  server: '服务端',
  client: '客户端',
}

/** 落地的种类（docs/03 §5.21）。 */
export const CHAIN_EXIT_KIND_META: Record<ChainExitKind, TagMeta> = {
  platform: { label: '平台出口', color: 'purple' },
  own: { label: '用户出口', color: 'gold' },
  node: { label: '平台节点' },
}

/** 阻断原因（docs/03 §5.18、§5.19）。 */
const BLOCKED_REASON_LABELS: Record<Exclude<ChainBlockedReason, ''>, string> = {
  plan: '套餐不支持',
  exit: '落地不可用',
  unsafe: '出口地址不允许',
  quarantined: '下发失败后被隔离',
}

/** 规则里每个起飞节点的状态（docs/03 §5.18、§5.19）；shadowed / duplicate 的说明由 entryStateMeta 补上序号。 */
const ENTRY_STATE_META: Record<ChainEntryState, TagMeta> = {
  inactive: { label: '未生效' },
  ok: { label: '进订阅', color: 'green' },
  unavailable: { label: '节点不可用', color: 'orange' },
  duplicate: { label: '重复', color: 'default' },
  shadowed: { label: '被覆盖', color: 'default' },
  applied: { label: '生效', color: 'green' },
  pending: { label: '下发中', color: 'blue' },
  failed: { label: '下发失败', color: 'red' },
}

const STATUS_OK = 'ok'
const NOT_COVERED = 0

/** 一个起飞节点的状态显示；被覆盖或重复时写明是被第几条规则。 */
export function entryStateMeta(state: ChainEntryState, coveredBy: number): TagMeta {
  const meta = ENTRY_STATE_META[state]
  const isCovered = (state === 'shadowed' || state === 'duplicate') && coveredBy !== NOT_COVERED
  return isCovered ? { ...meta, label: `${meta.label}（第 ${coveredBy} 条）` } : meta
}

/** 一条规则的状态显示：阻断（带原因）、已停用或通畅；各起飞节点的情况另见 entryStateMeta。 */
export function routeStateMeta(r: Pick<AdminChainRoute, 'status' | 'blockedReason' | 'isEnabled'>): TagMeta {
  if (r.status !== STATUS_OK) {
    const reason = r.blockedReason === '' ? '' : ` · ${BLOCKED_REASON_LABELS[r.blockedReason]}`
    return { label: `阻断${reason}`, color: 'orange' }
  }
  if (!r.isEnabled) {
    return { label: '已停用' }
  }
  return { label: '通畅', color: 'green' }
}
