import type { ChainEntryState, ChainExitKind, ChainKind, ChainRoute, ChainRouteEntry } from '../../api/client'

/** 起飞节点算“生效”的状态：客户端链式的可用、服务端链式的已生效与下发中（docs/06 链路示意卡）。 */
const ACTIVE_ENTRY_STATES: ReadonlySet<ChainEntryState> = new Set<ChainEntryState>(['ok', 'applied', 'pending'])
const STATUS_OK: ChainRoute['status'] = 'ok'

/** 起飞节点在某条连线上的状态记录（取自规则里的那一项，用来标节点状态）。 */
export type FlowEntryState = Pick<ChainRouteEntry, 'state' | 'coveredBy'>

/** 示意图里的一个起飞节点。 */
export interface FlowEntry {
  key: string
  nodeId: number
  name: string
  /** 至少有一条生效的连线。 */
  isActive: boolean
  /** 含这个起飞节点的规则 id，按规则顺序。 */
  routeIds: number[]
}

/** 示意图里的一个落地。 */
export interface FlowExit {
  key: string
  name: string
  kind: ChainExitKind
  protocol: string
  isActive: boolean
  /** 以它为落地的规则 id，按规则顺序。 */
  routeIds: number[]
}

/** 起飞节点到落地的一条连线；同一对只有一条，任一规则生效即生效。 */
export interface FlowLink {
  from: string
  to: string
  isActive: boolean
  /** 节点在这条线上的状态：取第一条生效规则里的那项，都不生效取第一条。 */
  entryState: FlowEntryState
  routeIds: number[]
}

export interface ChainFlow {
  entries: FlowEntry[]
  exits: FlowExit[]
  links: FlowLink[]
}

/** 起飞节点按节点去重；落地按“类别 + id”去重。 */
const entryKey = (nodeId: number) => `entry:${nodeId}`
const exitKey = (exit: ChainRoute['exit']) => `exit:${exit.kind}:${exit.id}`

const appendId = (ids: number[], id: number) => (ids.includes(id) ? ids : [...ids, id])

/**
 * 把一种链式的规则（已按顺序）整理成示意图数据，不改入参：
 * 起飞节点与落地按首次出现的顺序排列；规则通畅且启用、起飞节点处于生效类状态时连线算生效。
 */
export function buildChainFlow(routes: readonly ChainRoute[]): ChainFlow {
  const entries = new Map<string, FlowEntry>()
  const exits = new Map<string, FlowExit>()
  const links = new Map<string, FlowLink>()
  for (const route of routes) {
    const isRouteLive = route.status === STATUS_OK && route.isEnabled
    const toKey = exitKey(route.exit)
    const exit = exits.get(toKey) ?? { key: toKey, name: route.exit.name, kind: route.exit.kind, protocol: route.exit.protocol, isActive: false, routeIds: [] }
    exits.set(toKey, { ...exit, routeIds: appendId(exit.routeIds, route.id) })
    for (const e of route.entries) {
      const fromKey = entryKey(e.nodeId)
      const isActive = isRouteLive && ACTIVE_ENTRY_STATES.has(e.state)
      const linkKey = `${fromKey}>${toKey}`
      const entry = entries.get(fromKey) ?? { key: fromKey, nodeId: e.nodeId, name: e.name, isActive: false, routeIds: [] }
      entries.set(fromKey, { ...entry, isActive: entry.isActive || isActive, routeIds: appendId(entry.routeIds, route.id) })
      const state: FlowEntryState = { state: e.state, coveredBy: e.coveredBy }
      const link = links.get(linkKey)
      if (link === undefined) {
        links.set(linkKey, { from: fromKey, to: toKey, isActive, entryState: state, routeIds: [route.id] })
      } else {
        const isFirstActive = isActive && !link.isActive
        links.set(linkKey, { ...link, isActive: link.isActive || isActive, entryState: isFirstActive ? state : link.entryState, routeIds: appendId(link.routeIds, route.id) })
      }
      if (isActive) {
        const current = exits.get(toKey)
        if (current !== undefined) {
          exits.set(toKey, { ...current, isActive: true })
        }
      }
    }
  }
  return { entries: [...entries.values()], exits: [...exits.values()], links: [...links.values()] }
}

/** 画布尺寸（像素，画布内坐标）。 */
export const CANVAS_SIZE = {
  NODE_WIDTH: 200,
  NODE_HEIGHT: 56,
  GRID_GAP_X: 14,
  GRID_GAP_Y: 12,
  GROUP_PADDING: 14,
  GROUP_HEADER: 34,
  GROUP_GAP: 36,
  COLUMN_GAP: 120,
  MAX_COLUMNS: 4,
} as const

export const DEVICE_ID = 'device'
const groupId = (toKey: string) => `group:${toKey}`

export type CanvasNodeKind = 'device' | 'group' | 'entry' | 'exit'

/** 画布上的一个节点：子节点（区域里的起飞节点）坐标相对所在区域。 */
export interface CanvasNodeLayout {
  id: string
  kind: CanvasNodeKind
  parentId?: string
  x: number
  y: number
  width: number
  height: number
  isActive: boolean
  /** 点它时涉及的规则 id；设备为空。 */
  routeIds: number[]
  /** entry：起飞节点；group / exit：落地。 */
  entry?: FlowEntry
  entryState?: FlowEntryState
  exit?: FlowExit
  /** group：区域里的起飞节点个数。 */
  memberCount?: number
}

export interface CanvasEdgeLayout {
  id: string
  source: string
  target: string
  isActive: boolean
  routeIds: number[]
}

export interface CanvasLayout {
  nodes: CanvasNodeLayout[]
  edges: CanvasEdgeLayout[]
}

/** 每个起飞节点归到哪个落地的区域：第一条生效的连线，都不生效取第一条。 */
function homeLinks(links: readonly FlowLink[]): Map<string, FlowLink> {
  const home = new Map<string, FlowLink>()
  for (const link of links) {
    const current = home.get(link.from)
    if (current === undefined || (!current.isActive && link.isActive)) {
      home.set(link.from, link)
    }
  }
  return home
}

/** 区域网格的列数：大致排成方阵，最多 MAX_COLUMNS 列。 */
export function gridColumns(count: number): number {
  return Math.min(CANVAS_SIZE.MAX_COLUMNS, Math.max(1, Math.ceil(Math.sqrt(count))))
}

/** 区域的宽高：标题行 + 网格 + 内边距。 */
function groupSize(count: number): { width: number; height: number } {
  const { NODE_WIDTH, NODE_HEIGHT, GRID_GAP_X, GRID_GAP_Y, GROUP_PADDING, GROUP_HEADER } = CANVAS_SIZE
  const columns = gridColumns(count)
  const rows = Math.ceil(count / columns)
  return {
    width: GROUP_PADDING * 2 + columns * NODE_WIDTH + (columns - 1) * GRID_GAP_X,
    height: GROUP_HEADER + GROUP_PADDING + rows * NODE_HEIGHT + (rows - 1) * GRID_GAP_Y,
  }
}

/** 区域里第 index 个起飞节点的相对坐标。 */
function memberPosition(index: number, columns: number): { x: number; y: number } {
  const { NODE_WIDTH, NODE_HEIGHT, GRID_GAP_X, GRID_GAP_Y, GROUP_PADDING, GROUP_HEADER } = CANVAS_SIZE
  const column = index % columns
  const row = Math.floor(index / columns)
  return { x: GROUP_PADDING + column * (NODE_WIDTH + GRID_GAP_X), y: GROUP_HEADER + row * (NODE_HEIGHT + GRID_GAP_Y) }
}

const unionIds = (lists: readonly number[][]) => [...new Set(lists.flat())].sort((a, b) => a - b)

/**
 * 自动排版（Ruling-91）：设备在最左；每个落地一块区域纵向排开，区域里起飞节点排成网格；落地在区域右侧、与区域垂直居中。
 * 连线：设备 → 区域、区域 → 落地各一条（区域里任一起飞节点生效即生效）；起飞节点另走别的落地时从节点单独连过去。
 * 没有归到自己名下起飞节点的落地不画区域，只占一个节点高的位置。
 */
export function layoutChainFlow(flow: ChainFlow): CanvasLayout {
  const { NODE_WIDTH, NODE_HEIGHT, GROUP_GAP, COLUMN_GAP } = CANVAS_SIZE
  const home = homeLinks(flow.links)
  const entryByKey = new Map(flow.entries.map((e) => [e.key, e]))
  const members = new Map<string, FlowEntry[]>(flow.exits.map((x) => [x.key, []]))
  for (const entry of flow.entries) {
    const link = home.get(entry.key)
    if (link !== undefined) {
      members.get(link.to)?.push(entry)
    }
  }

  const slots = flow.exits.map((exit) => {
    const list = members.get(exit.key) ?? []
    const size = list.length > 0 ? groupSize(list.length) : { width: 0, height: NODE_HEIGHT }
    return { exit, list, ...size }
  })
  const groupLeft = NODE_WIDTH + COLUMN_GAP
  const widest = Math.max(0, ...slots.map((s) => s.width))
  const exitLeft = groupLeft + widest + COLUMN_GAP
  const totalHeight = slots.reduce((sum, s) => sum + s.height, 0) + Math.max(slots.length - 1, 0) * GROUP_GAP

  const nodes: CanvasNodeLayout[] = [
    { id: DEVICE_ID, kind: 'device', x: 0, y: (totalHeight - NODE_HEIGHT) / 2, width: NODE_WIDTH, height: NODE_HEIGHT, isActive: flow.entries.some((e) => e.isActive), routeIds: [] },
  ]
  const edges: CanvasEdgeLayout[] = []
  let top = 0
  for (const slot of slots) {
    const { exit, list } = slot
    const homeOf = (e: FlowEntry) => home.get(e.key)
    const isGroupActive = list.some((e) => homeOf(e)?.isActive === true)
    const groupRouteIds = unionIds(list.map((e) => homeOf(e)?.routeIds ?? []))
    if (list.length > 0) {
      const id = groupId(exit.key)
      const columns = gridColumns(list.length)
      nodes.push({ id, kind: 'group', x: groupLeft, y: top, width: slot.width, height: slot.height, isActive: isGroupActive, routeIds: exit.routeIds, exit, memberCount: list.length })
      list.forEach((entry, index) => {
        const pos = memberPosition(index, columns)
        nodes.push({ id: entry.key, kind: 'entry', parentId: id, ...pos, width: NODE_WIDTH, height: NODE_HEIGHT, isActive: entry.isActive, routeIds: entry.routeIds, entry, entryState: homeOf(entry)?.entryState })
      })
      edges.push({ id: `${DEVICE_ID}>${id}`, source: DEVICE_ID, target: id, isActive: isGroupActive, routeIds: groupRouteIds })
      edges.push({ id: `${id}>${exit.key}`, source: id, target: exit.key, isActive: isGroupActive, routeIds: groupRouteIds })
    }
    nodes.push({ id: exit.key, kind: 'exit', x: exitLeft, y: top + (slot.height - NODE_HEIGHT) / 2, width: NODE_WIDTH, height: NODE_HEIGHT, isActive: exit.isActive, routeIds: exit.routeIds, exit })
    top += slot.height + GROUP_GAP
  }
  // 起飞节点走的其它落地：从节点单独连过去
  for (const link of flow.links) {
    const isHome = home.get(link.from) === link
    if (!isHome && entryByKey.has(link.from)) {
      edges.push({ id: `${link.from}>${link.to}`, source: link.from, target: link.to, isActive: link.isActive, routeIds: link.routeIds })
    }
  }
  return { nodes, edges }
}

/** 用户拖动后的位置：键是节点 id（区域里的起飞节点为“区域 id/节点 id”，换了区域就不沿用）。 */
export type SavedPositions = Record<string, { x: number; y: number }>

const STORAGE_PREFIX = 'hub.chainFlow.positions.'

/** 保存位置用的键：子节点带上所在区域。 */
export function positionKey(node: Pick<CanvasNodeLayout, 'id' | 'parentId'>): string {
  return node.parentId === undefined ? node.id : `${node.parentId}/${node.id}`
}

const isPoint = (value: unknown): value is { x: number; y: number } =>
  typeof value === 'object' && value !== null && Number.isFinite((value as { x: unknown }).x) && Number.isFinite((value as { y: unknown }).y)

/** 读本浏览器里存的位置；没有、坏了或存储不可用时返回空表。 */
export function loadPositions(kind: ChainKind): SavedPositions {
  try {
    const raw = window.localStorage.getItem(STORAGE_PREFIX + kind)
    const parsed: unknown = raw === null ? null : JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null) {
      return {}
    }
    return Object.fromEntries(Object.entries(parsed).filter(([, v]) => isPoint(v))) as SavedPositions
  } catch {
    // 隐私模式、禁用存储或内容损坏：按自动排版显示即可，不打扰用户
    return {}
  }
}

/** 写入位置；存储不可用时静默放弃（下次按自动排版）。 */
export function savePositions(kind: ChainKind, positions: SavedPositions): void {
  try {
    window.localStorage.setItem(STORAGE_PREFIX + kind, JSON.stringify(positions))
  } catch {
    // 写不进去只是下次回到自动排版，不影响使用
  }
}

/** 清掉存的位置（“重新排列”）。 */
export function clearPositions(kind: ChainKind): void {
  try {
    window.localStorage.removeItem(STORAGE_PREFIX + kind)
  } catch {
    // 同上：存储不可用时本来就没有存过
  }
}

const clamp = (value: number, min: number, max: number) => Math.min(Math.max(value, min), Math.max(min, max))

/** 把存的位置套到自动排版上，不改入参；区域里的起飞节点限制在区域内（标题行以下）。 */
export function applySavedPositions(nodes: readonly CanvasNodeLayout[], saved: SavedPositions): CanvasNodeLayout[] {
  const parents = new Map(nodes.map((n) => [n.id, n]))
  return nodes.map((node) => {
    const point = saved[positionKey(node)]
    if (point === undefined) {
      return node
    }
    const parent = node.parentId === undefined ? undefined : parents.get(node.parentId)
    if (parent === undefined) {
      return { ...node, x: point.x, y: point.y }
    }
    return {
      ...node,
      x: clamp(point.x, 0, parent.width - node.width),
      y: clamp(point.y, CANVAS_SIZE.GROUP_HEADER, parent.height - node.height),
    }
  })
}

/**
 * 拖动期间不重排：规则数据在拖动途中刷新（下发中每 15 秒重取）时先记下，松手、位置存好之后再补一次，
 * 免得节点跳回旧位置。只在一个画布实例里用，不跨组件共享。
 */
export class RelayoutGate {
  private isDragging = false
  private hasPending = false

  /** 要重排：没在拖动就立即执行，拖动中则推迟到松手。 */
  request(relayout: () => void): void {
    if (this.isDragging) {
      this.hasPending = true
      return
    }
    relayout()
  }

  start(): void {
    this.isDragging = true
  }

  /** 松手：调用方先存好位置再调用；拖动中有推迟的重排就补上。 */
  finish(relayout: () => void): void {
    this.isDragging = false
    if (this.hasPending) {
      this.hasPending = false
      relayout()
    }
  }
}
