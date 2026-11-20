import { BaseEdge, getBezierPath, Handle, Position, type Edge, type EdgeProps, type Node, type NodeProps } from '@xyflow/react'
import { Globe, MonitorSmartphone, Server } from 'lucide-react'
import type { ReactNode } from 'react'
import { Flag } from '../../components/Flag'
import { useI18n } from '../../i18n/i18n-context'
import type { CanvasNodeLayout } from './chain-flow'
import { EXIT_KIND_KEYS, entryState } from './labels'

const ICON_SIZE = 16
/** 连线的可点击宽度（像素），比线本身宽，方便点中。 */
const EDGE_HIT_WIDTH = 18

/** 画布节点携带的数据：排版结果，另加设备块的说明与起飞节点的国家码。 */
export type ChainNodeData = CanvasNodeLayout & { deviceSub?: string; region?: string; hasCrossLink?: boolean } & Record<string, unknown>
export type ChainNode = Node<ChainNodeData>
export type ChainEdgeData = { isActive: boolean; routeIds: number[] } & Record<string, unknown>
export type ChainEdge = Edge<ChainEdgeData, 'chain'>

interface BlockProps {
  tone: 'device' | 'entry' | 'exit'
  icon: ReactNode
  title: ReactNode
  titleText: string
  subtitle: string
  statusTone?: string
  isActive: boolean
  hasInput: boolean
  hasOutput: boolean
}

/** 一块积木：左侧色块图标、名称与说明；两侧的连接点只用来挂线，不能拖出新线。 */
function Block({ tone, icon, title, titleText, subtitle, statusTone, isActive, hasInput, hasOutput }: BlockProps) {
  return (
    <div className={`hub-canvas-node hub-canvas-${tone}${isActive ? ' is-active' : ''}`} title={`${titleText} · ${subtitle}`}>
      {hasInput && <Handle type="target" position={Position.Left} isConnectable={false} />}
      <span className="hub-canvas-icon">{icon}</span>
      <span className="hub-canvas-text">
        <span className="hub-canvas-title">{typeof title === 'string' ? <span className="hub-canvas-name">{title}</span> : title}</span>
        <span className={`hub-canvas-sub${statusTone === undefined ? '' : ` is-${statusTone}`}`}>{subtitle}</span>
      </span>
      {hasOutput && <Handle type="source" position={Position.Right} isConnectable={false} />}
    </div>
  )
}

export function DeviceNode({ data }: NodeProps<ChainNode>) {
  const { t } = useI18n()
  const name = t('chain.flowDevice')
  return <Block tone="device" icon={<MonitorSmartphone size={ICON_SIZE} />} title={name} titleText={name} subtitle={data.deviceSub ?? ''} isActive={data.isActive} hasInput={false} hasOutput />
}

/** 起飞节点：国旗 + 名称，下面是它在所在区域那条线上的状态。 */
export function EntryNode({ data }: NodeProps<ChainNode>) {
  const { t } = useI18n()
  const name = data.entry?.name ?? ''
  const state = data.entryState === undefined ? null : entryState(t, data.entryState)
  const subtitle = state?.label ?? t(data.isActive ? 'chain.flowActive' : 'chain.flowInactive')
  const title = (
    <>
      {data.region !== undefined && <Flag code={data.region} />}
      <span className="hub-canvas-name">{name}</span>
    </>
  )
  // 只有另走别的落地时才露出右侧连接点，区域里一排排的点只会添乱
  return <Block tone="entry" icon={<Server size={ICON_SIZE} />} title={title} titleText={name} subtitle={subtitle} statusTone={state?.tone} isActive={data.isActive} hasInput={false} hasOutput={data.hasCrossLink === true} />
}

export function ExitNode({ data }: NodeProps<ChainNode>) {
  const { t } = useI18n()
  const exit = data.exit
  const name = exit?.name ?? ''
  const kind = exit === undefined ? '' : t(EXIT_KIND_KEYS[exit.kind])
  const subtitle = exit === undefined || exit.protocol === '' ? kind : `${kind} · ${exit.protocol}`
  return <Block tone="exit" icon={<Globe size={ICON_SIZE} />} title={name} titleText={name} subtitle={subtitle} isActive={data.isActive} hasInput hasOutput={false} />
}

/** 一个落地的区域：标题行写落地名与起飞节点个数，里面是起飞节点的网格。 */
export function GroupNode({ data }: NodeProps<ChainNode>) {
  const { t } = useI18n()
  return (
    <div className={`hub-canvas-group${data.isActive ? ' is-active' : ''}`}>
      <Handle type="target" position={Position.Left} isConnectable={false} />
      <div className="hub-canvas-group-head">
        <Globe size={14} aria-hidden="true" />
        <span className="hub-canvas-name">{data.exit?.name ?? ''}</span>
        <span className="hub-canvas-group-count">{t('chain.flowGroupCount', { n: data.memberCount ?? 0 })}</span>
      </div>
      <Handle type="source" position={Position.Right} isConnectable={false} />
    </div>
  )
}

/** 连线：生效的实线并叠一层流动的光点，其余灰色虚线（docs/06 链路示意卡）。 */
export function ChainEdgeView({ id, sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition, data }: EdgeProps<ChainEdge>) {
  const [path] = getBezierPath({ sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition })
  const isActive = data?.isActive === true
  return (
    <>
      <BaseEdge id={id} path={path} className={`hub-canvas-link${isActive ? ' is-active' : ''}`} interactionWidth={EDGE_HIT_WIDTH} />
      {isActive && <path d={path} className="hub-flow-pulse" />}
    </>
  )
}
