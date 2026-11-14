import type { ChainEntryState, ChainExitKind, ChainRoute } from '../../api/client'

/** 起飞节点算“生效”的状态：客户端链式的可用、服务端链式的已生效与下发中（docs/06 链路示意卡）。 */
const ACTIVE_ENTRY_STATES: ReadonlySet<ChainEntryState> = new Set<ChainEntryState>(['ok', 'applied', 'pending'])
const STATUS_OK: ChainRoute['status'] = 'ok'

/** 示意图里的一块积木（起飞节点或落地）。 */
export interface FlowBlock {
  key: string
  name: string
  /** 至少有一条生效的连线。 */
  isActive: boolean
}

/** 落地积木另带类别与协议。 */
export interface FlowExitBlock extends FlowBlock {
  kind: ChainExitKind
  protocol: string
}

/** 起飞节点到落地的一条连线；同一对只有一条，任一规则生效即生效。 */
export interface FlowLink {
  from: string
  to: string
  isActive: boolean
}

export interface ChainFlow {
  entries: FlowBlock[]
  exits: FlowExitBlock[]
  links: FlowLink[]
}

/** 起飞节点按节点去重；落地按“类别 + id”去重。 */
const entryKey = (nodeId: number) => `entry:${nodeId}`
const exitKey = (exit: ChainRoute['exit']) => `exit:${exit.kind}:${exit.id}`

/**
 * 把一种链式的规则（已按顺序）整理成示意图的三列数据，不改入参：
 * 起飞节点与落地按首次出现的顺序排列；规则通畅且启用、起飞节点处于生效类状态时连线算生效。
 */
export function buildChainFlow(routes: readonly ChainRoute[]): ChainFlow {
  const entries = new Map<string, FlowBlock>()
  const exits = new Map<string, FlowExitBlock>()
  const links = new Map<string, FlowLink>()
  for (const route of routes) {
    const isRouteLive = route.status === STATUS_OK && route.isEnabled
    const toKey = exitKey(route.exit)
    if (!exits.has(toKey)) {
      exits.set(toKey, { key: toKey, name: route.exit.name, kind: route.exit.kind, protocol: route.exit.protocol, isActive: false })
    }
    for (const e of route.entries) {
      const fromKey = entryKey(e.nodeId)
      const isActive = isRouteLive && ACTIVE_ENTRY_STATES.has(e.state)
      const linkKey = `${fromKey}>${toKey}`
      entries.set(fromKey, { key: fromKey, name: e.name, isActive: (entries.get(fromKey)?.isActive ?? false) || isActive })
      links.set(linkKey, { from: fromKey, to: toKey, isActive: (links.get(linkKey)?.isActive ?? false) || isActive })
      const exit = exits.get(toKey)
      if (exit !== undefined && isActive) {
        exits.set(toKey, { ...exit, isActive: true })
      }
    }
  }
  return { entries: [...entries.values()], exits: [...exits.values()], links: [...links.values()] }
}

/** 示意图尺寸（SVG 坐标，按 viewBox 缩放）。 */
export const FLOW_SIZE = {
  NODE_WIDTH: 196,
  NODE_HEIGHT: 58,
  ROW_GAP: 18,
  COLUMN_GAP: 110,
  PADDING: 14,
} as const

/** 一列里第 index 块的左上角纵坐标：各列在总高度里垂直居中。 */
export function rowTop(count: number, index: number, totalHeight: number): number {
  const { NODE_HEIGHT, ROW_GAP, PADDING } = FLOW_SIZE
  const columnHeight = count * NODE_HEIGHT + Math.max(count - 1, 0) * ROW_GAP
  return PADDING + (totalHeight - columnHeight) / 2 + index * (NODE_HEIGHT + ROW_GAP)
}

/** 内容区高度：最多的一列决定。 */
export function contentHeight(maxCount: number): number {
  const { NODE_HEIGHT, ROW_GAP } = FLOW_SIZE
  const rows = Math.max(maxCount, 1)
  return rows * NODE_HEIGHT + (rows - 1) * ROW_GAP
}

/** 两点之间的平滑曲线（三次贝塞尔，两端水平切入）。 */
export function curvePath(x1: number, y1: number, x2: number, y2: number): string {
  const dx = (x2 - x1) / 2
  return `M ${x1} ${y1} C ${x1 + dx} ${y1}, ${x2 - dx} ${y2}, ${x2} ${y2}`
}

/** 中日韩等全角字符按 2 个宽度计。 */
const WIDE_CHAR_PATTERN = /[⺀-鿿가-힯＀-￯]/

/** 按显示宽度截断，超出时末尾加省略号；maxUnits 以半角字符计。 */
export function truncateByWidth(text: string, maxUnits: number): string {
  let units = 0
  let out = ''
  for (const ch of text) {
    const width = WIDE_CHAR_PATTERN.test(ch) ? 2 : 1
    if (units + width > maxUnits) {
      return `${out}…`
    }
    units += width
    out += ch
  }
  return out
}
