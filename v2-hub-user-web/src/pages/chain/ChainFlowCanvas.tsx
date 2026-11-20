import '@xyflow/react/dist/style.css'
import {
  Controls,
  MiniMap,
  Panel,
  ReactFlow,
  ReactFlowProvider,
  Background,
  BackgroundVariant,
  useNodesState,
  useReactFlow,
  type AriaLabelConfig,
  type NodeMouseHandler,
  type EdgeMouseHandler,
  type OnNodeDrag,
} from '@xyflow/react'
import { LayoutGrid, Maximize2, Minimize2, X } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { ChainKind, ChainRoute } from '../../api/client'
import { Badge, Button } from '../../components/ui'
import { useI18n } from '../../i18n/i18n-context'
import {
  applySavedPositions,
  buildChainFlow,
  clearPositions,
  layoutChainFlow,
  loadPositions,
  positionKey,
  RelayoutGate,
  savePositions,
  type CanvasNodeLayout,
  type SavedPositions,
} from './chain-flow'
import { ChainEdgeView, DeviceNode, EntryNode, ExitNode, GroupNode, type ChainEdge, type ChainNode } from './ChainFlowNodes'
import { routeState } from './labels'

const ESCAPE_KEY = 'Escape'
const MIN_ZOOM = 0.2
const MAX_ZOOM = 1.5
const FIT_PADDING = 0.12
/** 规则少时不把节点放得比原尺寸还大。 */
const FIT_MAX_ZOOM = 1
const FIT_DURATION_MS = 300
/** 拖动超过这个距离（像素）才算拖动，否则算点击。 */
const DRAG_THRESHOLD = 4
const BACKGROUND_GAP = 20
/** 画布尺寸变了（转屏、拖窗口、进出全屏）之后多久重新适应，避免拖动窗口时连续触发。 */
const RESIZE_REFIT_DELAY_MS = 150
const ICON_SIZE = 15
/** 区域用 area 而不用 React Flow 自带的 group 类型，免得带上它的默认边框与底色。 */
const AREA_TYPE = 'area'
const NODE_TYPES = { device: DeviceNode, entry: EntryNode, exit: ExitNode, [AREA_TYPE]: GroupNode }
const EDGE_TYPES = { chain: ChainEdgeView }

export interface ChainFlowCanvasProps {
  kind: ChainKind
  routes: readonly ChainRoute[]
  /** 节点 id → 所在服务器的国家码。 */
  regions: ReadonlyMap<number, string>
  deviceSub: string
  /** 套餐开了这类链式，才能打开编辑框。 */
  isWritable: boolean
  onEdit: (route: ChainRoute) => void
  /** 规则编辑框开着：Esc 留给编辑框，不退出全屏。 */
  isEditing: boolean
}

interface RoutePick {
  title: string
  routeIds: number[]
}

function toFlowNode(n: CanvasNodeLayout, regions: ReadonlyMap<number, string>, deviceSub: string, crossSources: ReadonlySet<string>): ChainNode {
  const region = n.entry === undefined ? undefined : regions.get(n.entry.nodeId)
  return {
    id: n.id,
    type: n.kind === 'group' ? AREA_TYPE : n.kind,
    position: { x: n.x, y: n.y },
    width: n.width,
    height: n.height,
    ...(n.parentId === undefined ? {} : { parentId: n.parentId, extent: 'parent' as const }),
    data: { ...n, deviceSub, hasCrossLink: crossSources.has(n.id), ...(region === undefined ? {} : { region }) },
  }
}

/** 链路画布（Ruling-91）：卡片里按需加载的部分，默认导出给 React.lazy。 */
export default function ChainFlowCanvas(props: ChainFlowCanvasProps) {
  return (
    <ReactFlowProvider>
      <Canvas {...props} />
    </ReactFlowProvider>
  )
}

function Canvas({ kind, routes, regions, deviceSub, isWritable, onEdit, isEditing }: ChainFlowCanvasProps) {
  const { t } = useI18n()
  const { fitView, getNodes } = useReactFlow<ChainNode, ChainEdge>()
  const layout = useMemo(() => layoutChainFlow(buildChainFlow(routes)), [routes])
  const toNodes = useCallback(
    (saved: SavedPositions) => {
      const crossSources = new Set(layout.edges.map((e) => e.source))
      return applySavedPositions(layout.nodes, saved).map((n) => toFlowNode(n, regions, deviceSub, crossSources))
    },
    [layout, regions, deviceSub],
  )
  const [nodes, setNodes, onNodesChange] = useNodesState<ChainNode>(toNodes(loadPositions(kind)))
  const [isFullscreen, setIsFullscreen] = useState(false)
  const [pick, setPick] = useState<RoutePick | null>(null)

  const [gate] = useState(() => new RelayoutGate())
  const relayoutFromStorage = useCallback(() => setNodes(toNodes(loadPositions(kind))), [setNodes, toNodes, kind])

  // 规则重取（含下发中的定时刷新）后按新数据重排，用户拖过的位置从本浏览器里取回；拖动途中推迟到松手
  useEffect(() => {
    gate.request(relayoutFromStorage)
  }, [gate, relayoutFromStorage])

  const edges = useMemo<ChainEdge[]>(
    () =>
      [...layout.edges]
        .sort((a, b) => Number(a.isActive) - Number(b.isActive))
        .map((e) => ({ id: e.id, source: e.source, target: e.target, type: 'chain', data: { isActive: e.isActive, routeIds: e.routeIds } })),
    [layout],
  )

  const refit = useCallback(() => {
    window.requestAnimationFrame(() => void fitView({ padding: FIT_PADDING, maxZoom: FIT_MAX_ZOOM, duration: FIT_DURATION_MS }))
  }, [fitView])

  // 画布尺寸变了就重新适应，免得内容跑到可视区外
  const hostRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const host = hostRef.current
    if (host === null || typeof ResizeObserver === 'undefined') {
      return undefined
    }
    let timer: number | undefined
    let lastWidth = host.clientWidth
    let lastHeight = host.clientHeight
    const observer = new ResizeObserver(() => {
      const isSameSize = host.clientWidth === lastWidth && host.clientHeight === lastHeight
      if (isSameSize) {
        return
      }
      lastWidth = host.clientWidth
      lastHeight = host.clientHeight
      window.clearTimeout(timer)
      timer = window.setTimeout(refit, RESIZE_REFIT_DELAY_MS)
    })
    observer.observe(host)
    return () => {
      observer.disconnect()
      window.clearTimeout(timer)
    }
  }, [refit])

  // 全屏时 Esc 退出
  useEffect(() => {
    if (!isFullscreen || isEditing) {
      return undefined
    }
    const handleKey = (event: KeyboardEvent) => {
      if (event.key === ESCAPE_KEY) {
        setIsFullscreen(false)
      }
    }
    window.addEventListener('keydown', handleKey)
    return () => window.removeEventListener('keydown', handleKey)
  }, [isFullscreen, isEditing])

  // 进出全屏改变画布尺寸，由上面的尺寸监听重新适应
  const toggleFullscreen = () => setIsFullscreen((v) => !v)

  const relayout = () => {
    clearPositions(kind)
    setNodes(toNodes({}))
    refit()
  }

  // 只记被拖动的节点，其余仍跟自动排版走（新加的节点不会被旧位置挤住）
  const handleDragStop: OnNodeDrag<ChainNode> = (_event, _node, dragged) => {
    const latest = new Map(getNodes().map((n) => [n.id, n]))
    const moved = Object.fromEntries(
      dragged.map((d) => {
        const n = latest.get(d.id) ?? d
        return [positionKey({ id: n.id, parentId: n.parentId }), { x: n.position.x, y: n.position.y }]
      }),
    )
    savePositions(kind, { ...loadPositions(kind), ...moved })
    gate.finish(relayoutFromStorage)
  }

  const routeById = useMemo(() => new Map(routes.map((r) => [r.id, r])), [routes])

  /** 相关规则只有一条且可编辑时直接打开编辑框，否则列出来选。 */
  const select = (title: string, routeIds: number[]) => {
    const only = routeIds.length === 1 ? routeById.get(routeIds[0] ?? 0) : undefined
    if (only !== undefined && isWritable) {
      setPick(null)
      onEdit(only)
      return
    }
    setPick(routeIds.length === 0 ? null : { title, routeIds })
  }

  const handleNodeClick: NodeMouseHandler<ChainNode> = (_event, node) => {
    const name = node.data.entry?.name ?? node.data.exit?.name ?? ''
    select(name, node.data.routeIds)
  }

  const handleEdgeClick: EdgeMouseHandler<ChainEdge> = (_event, edge) => select(t('chain.flowPickLink'), edge.data?.routeIds ?? [])

  const ariaLabels: Partial<AriaLabelConfig> = {
    'controls.ariaLabel': t('chain.flowControls'),
    'controls.zoomIn.ariaLabel': t('chain.flowZoomIn'),
    'controls.zoomOut.ariaLabel': t('chain.flowZoomOut'),
    'controls.fitView.ariaLabel': t('chain.flowFitView'),
    'minimap.ariaLabel': t('chain.flowMinimap'),
  }

  const picked = pick === null ? [] : pick.routeIds.flatMap((id) => routeById.get(id) ?? [])
  const summary = t('chain.flowSummary', { entries: layout.nodes.filter((n) => n.kind === 'entry').length, exits: layout.nodes.filter((n) => n.kind === 'exit').length })

  return (
    <div ref={hostRef} className={`hub-canvas${isFullscreen ? ' is-fullscreen' : ''}`} role="group" aria-label={summary}>
      <ReactFlow<ChainNode, ChainEdge>
        nodes={nodes}
        edges={edges}
        nodeTypes={NODE_TYPES}
        edgeTypes={EDGE_TYPES}
        onNodesChange={onNodesChange}
        onNodeDragStart={() => gate.start()}
        onNodeDragStop={handleDragStop}
        onNodeClick={handleNodeClick}
        onEdgeClick={handleEdgeClick}
        onPaneClick={() => setPick(null)}
        nodesConnectable={false}
        deleteKeyCode={null}
        nodeDragThreshold={DRAG_THRESHOLD}
        zoomOnScroll={isFullscreen}
        preventScrolling={isFullscreen}
        zoomOnDoubleClick={false}
        minZoom={MIN_ZOOM}
        maxZoom={MAX_ZOOM}
        fitView
        fitViewOptions={{ padding: FIT_PADDING, maxZoom: FIT_MAX_ZOOM }}
        colorMode="light"
        attributionPosition="bottom-center"
        ariaLabelConfig={ariaLabels}
      >
        <Background variant={BackgroundVariant.Dots} gap={BACKGROUND_GAP} />
        <Controls showInteractive={false} fitViewOptions={{ padding: FIT_PADDING, duration: FIT_DURATION_MS }} />
        {/* 卡片里本来就整体适应画布，缩略图只在全屏时出，免得挡住落地 */}
        {isFullscreen && <MiniMap className="hub-canvas-minimap" pannable zoomable />}
        <Panel position="top-right" className="hub-canvas-toolbar">
          {/* 窄屏只留图标，文字仍作按钮名称供读屏 */}
          <Button variant="outline" onClick={relayout}>
            <LayoutGrid size={ICON_SIZE} aria-hidden="true" />
            <span className="hub-canvas-btn-text">{t('chain.flowRelayout')}</span>
          </Button>
          <Button variant="outline" aria-pressed={isFullscreen} onClick={toggleFullscreen}>
            {isFullscreen ? <Minimize2 size={ICON_SIZE} aria-hidden="true" /> : <Maximize2 size={ICON_SIZE} aria-hidden="true" />}
            <span className="hub-canvas-btn-text">{t(isFullscreen ? 'chain.flowExitFullscreen' : 'chain.flowFullscreen')}</span>
          </Button>
        </Panel>
        {pick !== null && (
          <Panel position="top-left" className="hub-canvas-pick" aria-label={t('chain.flowPickTitle', { n: picked.length })}>
            <div className="hub-canvas-pick-head">
              <span className="hub-canvas-name">{pick.title}</span>
              <button type="button" className="hub-canvas-pick-close" aria-label={t('chain.flowPickClose')} onClick={() => setPick(null)}>
                <X size={ICON_SIZE} aria-hidden="true" />
              </button>
            </div>
            <p className="hub-field-hint">{t(isWritable ? 'chain.flowPickTitle' : 'chain.flowPickReadonly', { n: picked.length })}</p>
            <ul className="hub-canvas-pick-list">
              {picked.map((route) => {
                const state = routeState(t, route)
                return (
                  <li key={route.id}>
                    <span className="hub-route-position">{t('chain.position', { n: route.position })}</span>
                    <span className="hub-canvas-name">{route.displayName}</span>
                    <Badge tone={state.tone}>{state.label}</Badge>
                    <Button variant="outline" disabled={!isWritable} onClick={() => {
                      setPick(null)
                      onEdit(route)
                    }}>
                      {t('chain.edit')}
                    </Button>
                  </li>
                )
              })}
            </ul>
          </Panel>
        )}
      </ReactFlow>
    </div>
  )
}
