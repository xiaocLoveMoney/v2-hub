import { Globe, MonitorSmartphone, Server } from 'lucide-react'
import type { ReactNode } from 'react'
import { api, type ChainKind, type ChainRoute, type Dashboard } from '../../api/client'
import { Card, Empty } from '../../components/ui'
import { useRemote } from '../../hooks/use-remote'
import { useI18n } from '../../i18n/i18n-context'
import { buildChainFlow, contentHeight, curvePath, FLOW_SIZE, rowTop, truncateByWidth, type FlowLink } from './chain-flow'
import { EXIT_KIND_KEYS } from './labels'

const KIND_SERVER: ChainKind = 'server'
const ICON_SIZE = 18
const ICON_BOX = 32
const ICON_INSET = 13
const TEXT_LEFT = ICON_INSET + ICON_BOX + 11
const TITLE_BASELINE = 25
const SUB_BASELINE = 43
const HANDLE_RADIUS = 4
/** 积木里文字的最大显示宽度（半角字符数）。 */
const TITLE_MAX_UNITS = 18
const SUB_MAX_UNITS = 22
const COLUMN_COUNT = 3

const loadDashboard = (signal: AbortSignal) => api.GET('/api/user/dashboard', { signal })

/** 三列的左边横坐标：设备、起飞节点、落地。 */
const columnLeft = (column: number) => FLOW_SIZE.PADDING + column * (FLOW_SIZE.NODE_WIDTH + FLOW_SIZE.COLUMN_GAP)

type BlockTone = 'device' | 'entry' | 'exit'

interface BlockProps {
  x: number
  y: number
  tone: BlockTone
  icon: ReactNode
  title: string
  subtitle: string
  isActive: boolean
  hasInput: boolean
  hasOutput: boolean
}

/** 一块积木：圆角卡片、左侧色块图标、名称与说明，两侧带连接点。 */
function Block({ x, y, tone, icon, title, subtitle, isActive, hasInput, hasOutput }: BlockProps) {
  const { NODE_WIDTH, NODE_HEIGHT } = FLOW_SIZE
  const middle = NODE_HEIGHT / 2
  return (
    <g transform={`translate(${x} ${y})`} className={`hub-flow-block hub-flow-${tone}${isActive ? ' is-active' : ''}`}>
      <title>{`${title} · ${subtitle}`}</title>
      <rect className="hub-flow-block-bg" width={NODE_WIDTH} height={NODE_HEIGHT} rx={8} />
      <rect className="hub-flow-icon-bg" x={ICON_INSET} y={(NODE_HEIGHT - ICON_BOX) / 2} width={ICON_BOX} height={ICON_BOX} rx={6} />
      <g transform={`translate(${ICON_INSET + (ICON_BOX - ICON_SIZE) / 2} ${(NODE_HEIGHT - ICON_SIZE) / 2})`} className="hub-flow-icon">
        {icon}
      </g>
      <text className="hub-flow-title" x={TEXT_LEFT} y={TITLE_BASELINE}>
        {truncateByWidth(title, TITLE_MAX_UNITS)}
      </text>
      <text className="hub-flow-sub" x={TEXT_LEFT} y={SUB_BASELINE}>
        {truncateByWidth(subtitle, SUB_MAX_UNITS)}
      </text>
      {hasInput && <circle className="hub-flow-handle" cx={0} cy={middle} r={HANDLE_RADIUS} />}
      {hasOutput && <circle className="hub-flow-handle" cx={NODE_WIDTH} cy={middle} r={HANDLE_RADIUS} />}
    </g>
  )
}

interface ChainFlowCardProps {
  kind: ChainKind
  routes: readonly ChainRoute[]
}

/**
 * 链路示意卡（docs/06 链路示意卡）：跟随当前页签，把规则画成“我的设备 → 起飞节点 → 落地”三列积木，平滑曲线相连；
 * 生效的连线为实线并流动，其余为灰色虚线。纯前端按已有数据绘制。
 */
export function ChainFlowCard({ kind, routes }: ChainFlowCardProps) {
  const { t } = useI18n()
  const dashboard = useRemote<Dashboard>(loadDashboard)
  const flow = buildChainFlow(routes)
  const title = t(kind === KIND_SERVER ? 'chain.flowTitleServer' : 'chain.flowTitleClient')
  if (flow.entries.length === 0) {
    return (
      <Card title={title}>
        <Empty>{t('chain.flowEmpty')}</Empty>
      </Card>
    )
  }

  const { NODE_WIDTH, NODE_HEIGHT, PADDING } = FLOW_SIZE
  const inner = contentHeight(Math.max(flow.entries.length, flow.exits.length))
  const width = PADDING * 2 + COLUMN_COUNT * NODE_WIDTH + (COLUMN_COUNT - 1) * FLOW_SIZE.COLUMN_GAP
  const height = inner + PADDING * 2
  const middle = NODE_HEIGHT / 2
  const deviceY = rowTop(1, 0, inner)
  const entryY = new Map(flow.entries.map((e, i) => [e.key, rowTop(flow.entries.length, i, inner)]))
  const exitY = new Map(flow.exits.map((e, i) => [e.key, rowTop(flow.exits.length, i, inner)]))
  const online = dashboard.data?.onlineDevices
  const deviceSub = online === null || online === undefined ? t('chain.flowDeviceUnknown') : t('chain.flowDeviceOnline', { n: online })

  // 设备到每个起飞节点一条线；起飞节点到落地按整理好的连线。灰线先画，生效的画在上面
  const deviceLinks: FlowLink[] = flow.entries.map((e) => ({ from: 'device', to: e.key, isActive: e.isActive }))
  const pathOf = (link: FlowLink) => {
    const isFromDevice = link.from === 'device'
    const y1 = (isFromDevice ? deviceY : (entryY.get(link.from) ?? 0)) + middle
    const y2 = (isFromDevice ? (entryY.get(link.to) ?? 0) : (exitY.get(link.to) ?? 0)) + middle
    const x1 = columnLeft(isFromDevice ? 0 : 1) + NODE_WIDTH
    const x2 = columnLeft(isFromDevice ? 1 : 2)
    return curvePath(x1, y1, x2, y2)
  }
  const allLinks = [...deviceLinks, ...flow.links].sort((a, b) => Number(a.isActive) - Number(b.isActive))

  return (
    <Card title={title}>
      <div className="hub-flow-scroll">
        <svg
          className="hub-flow"
          viewBox={`0 0 ${width} ${height}`}
          width={width}
          role="img"
          aria-label={t('chain.flowSummary', { entries: flow.entries.length, exits: flow.exits.length })}
        >
          <g className="hub-flow-links">
            {allLinks.map((link) => {
              const d = pathOf(link)
              return (
                <g key={`${link.from}>${link.to}`}>
                  <path d={d} className={`hub-flow-link${link.isActive ? ' is-active' : ''}`} />
                  {link.isActive && <path d={d} className="hub-flow-pulse" />}
                </g>
              )
            })}
          </g>
          <Block
            x={columnLeft(0)}
            y={deviceY}
            tone="device"
            icon={<MonitorSmartphone size={ICON_SIZE} />}
            title={t('chain.flowDevice')}
            subtitle={deviceSub}
            isActive={flow.entries.some((e) => e.isActive)}
            hasInput={false}
            hasOutput
          />
          {flow.entries.map((e) => (
            <Block
              key={e.key}
              x={columnLeft(1)}
              y={entryY.get(e.key) ?? 0}
              tone="entry"
              icon={<Server size={ICON_SIZE} />}
              title={e.name}
              subtitle={t(e.isActive ? 'chain.flowActive' : 'chain.flowInactive')}
              isActive={e.isActive}
              hasInput
              hasOutput
            />
          ))}
          {flow.exits.map((e) => (
            <Block
              key={e.key}
              x={columnLeft(2)}
              y={exitY.get(e.key) ?? 0}
              tone="exit"
              icon={<Globe size={ICON_SIZE} />}
              title={e.name}
              subtitle={e.protocol === '' ? t(EXIT_KIND_KEYS[e.kind]) : `${t(EXIT_KIND_KEYS[e.kind])} · ${e.protocol}`}
              isActive={e.isActive}
              hasInput
              hasOutput={false}
            />
          ))}
        </svg>
      </div>
      <div className="hub-flow-legend">
        <span className="hub-flow-legend-item">
          <span className="hub-flow-swatch is-active" aria-hidden="true" />
          {t('chain.flowLegendActive')}
        </span>
        <span className="hub-flow-legend-item">
          <span className="hub-flow-swatch" aria-hidden="true" />
          {t('chain.flowLegendInactive')}
        </span>
      </div>
    </Card>
  )
}
