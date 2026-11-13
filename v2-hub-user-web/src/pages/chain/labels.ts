import type { ChainBlockedReason, ChainRule } from '../../api/client'

/** 名称上限，与 hub-api.yaml 的 ChainRuleInput / CustomNodeInput 一致。 */
export const MAX_CHAIN_NAME_LEN = 64
/** 分享链接上限，与 hub-api.yaml 的 CustomNodeInput.link 一致。 */
export const MAX_CUSTOM_LINK_LEN = 2048

/** 阻断原因的文案（docs/03 §5.18）。 */
export const BLOCKED_REASON_LABELS: Record<Exclude<ChainBlockedReason, ''>, string> = {
  plan: '套餐不支持',
  entry: '入口不可用',
  exit: '落地不可用',
}

const STATUS_OK: ChainRule['status'] = 'ok'
// eslint-disable-next-line no-control-regex -- 就是要拒绝控制字符
const CONTROL_CHAR_PATTERN = /[\u0000-\u001f\u007f]/

/** 规则状态的显示文案：通畅，或“阻断 · 原因”。 */
export function chainStatusLabel(rule: ChainRule): string {
  if (rule.status === STATUS_OK) {
    return '通畅'
  }
  const reason = rule.blockedReason === '' ? '' : BLOCKED_REASON_LABELS[rule.blockedReason]
  return reason === '' ? '阻断' : `阻断 · ${reason}`
}

/** 规则是否通畅。 */
export function isChainOk(rule: ChainRule): boolean {
  return rule.status === STATUS_OK
}

/** 名称是否合法：不超过上限、不含换行等控制字符（与后端校验一致）。 */
export function isValidChainName(name: string): boolean {
  // 按字符计数，与后端按 rune 计数一致
  const length = [...name].length
  return length <= MAX_CHAIN_NAME_LEN && !CONTROL_CHAR_PATTERN.test(name)
}
