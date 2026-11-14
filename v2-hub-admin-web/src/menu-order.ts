/** 可排序的菜单项：菜单键与所属分组（未设置 = 顶层）。 */
export interface OrderableEntry {
  key: string
  group?: string
}

/** 顶层菜单项（不属于任何分组）视作一组时用的组名。 */
const TOP_GROUP = ''

/**
 * 按菜单顺序设置排菜单（docs/03 §5.6 菜单顺序，Ruling-57），返回新数组、不改原表：
 * 列表里有的项按列表顺序在前，没列到的按表内默认顺序在后；分组的先后取组内最靠前的一项；不认识的键忽略。
 */
export function applyMenuOrder<T extends OrderableEntry>(entries: readonly T[], order: readonly string[]): T[] {
  const position = new Map(order.map((key, index) => [key, index]))
  const ranked = entries.map((entry, index) => ({ entry, group: entry.group ?? TOP_GROUP, rank: position.get(entry.key) ?? order.length + index }))
  const groupRank = new Map<string, number>()
  for (const r of ranked) {
    groupRank.set(r.group, Math.min(groupRank.get(r.group) ?? r.rank, r.rank))
  }
  const rankOf = (group: string) => groupRank.get(group) ?? 0
  return ranked.sort((a, b) => rankOf(a.group) - rankOf(b.group) || a.rank - b.rank).map((r) => r.entry)
}
