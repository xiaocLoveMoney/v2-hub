// 节点显示名的前缀 / 后缀（docs/03 §5.1，Ruling-80）；上限与整理方式和后端校验一致

/** 前缀、后缀各自的最大字符数。 */
export const MAX_NODE_AFFIX_LEN = 32

/** 节点显示名的最大字符数。 */
export const MAX_NODE_NAME_LEN = 128

/** 一次批量修改的节点数上限。 */
export const MAX_AFFIX_NODES = 500

/** 要加到显示名两头的前缀与后缀；空串为不加。 */
export interface NodeAffix {
  prefix: string
  suffix: string
}

export const EMPTY_NODE_AFFIX: NodeAffix = { prefix: '', suffix: '' }

/**
 * 整理前后缀：前缀去掉开头的空白、后缀去掉结尾的空白，中间用来分隔的空格保留。
 * @param affix 输入框里的原样内容
 * @returns 提交给后端的前后缀
 */
export function normalizeAffix(affix: NodeAffix): NodeAffix {
  return { prefix: affix.prefix.trimStart(), suffix: affix.suffix.trimEnd() }
}

/** 前后缀是否都没填（整理之后）。 */
export function isAffixEmpty(affix: NodeAffix): boolean {
  const { prefix, suffix } = normalizeAffix(affix)
  return prefix === '' && suffix === ''
}

/**
 * 显示名加上前后缀之后的样子。
 * @param name 现有显示名
 * @param affix 前后缀（未整理的也可以）
 */
export function affixName(name: string, affix: NodeAffix): string {
  const { prefix, suffix } = normalizeAffix(affix)
  return `${prefix}${name}${suffix}`
}

/** 按字符（码点）数算长度，与后端、数据库的算法一致；emoji 这类字符算 1 个而不是 2 个。 */
export function charCount(text: string): number {
  return Array.from(text).length
}

/** 加上前后缀后是否超过显示名上限。 */
export function isAffixedNameTooLong(name: string, affix: NodeAffix): boolean {
  return charCount(affixName(name, affix)) > MAX_NODE_NAME_LEN
}
