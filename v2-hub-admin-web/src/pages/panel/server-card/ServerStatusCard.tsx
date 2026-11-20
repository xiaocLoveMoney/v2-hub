import {
  ArrowDownOutlined,
  ArrowUpOutlined,
  CalendarOutlined,
  ClockCircleOutlined,
  DatabaseOutlined,
  DownOutlined,
  EditOutlined,
  LineChartOutlined,
  UpOutlined,
} from '@ant-design/icons'
import { Button, Card, Tag, Tooltip } from 'antd'
import { useState, type ReactNode } from 'react'
import { Link, useNavigate } from 'react-router'
import type { LatencyPoint, LatencySeries, Server, ServerTrend } from '../../../api/client'
import { formatTime } from '../../system/format'
import { countryName } from '../countries'
import { ServerStatusTag } from '../status'
import { ServerCardDetails } from './ServerCardDetails'
import { HistoryBar, SegmentBar, type HistoryCell } from './SegmentBar'
import {
  daysBetween,
  formatBytes,
  formatPercent,
  formatPrice,
  formatRate,
  formatUptime,
  latencyLevel,
  localToday,
  lossLevel,
  percentOf,
  usageLevel,
} from './server-card-format'

/** 历史条与速率小图的格数，与接口给的点数一致（hub-api ServerTrend）。 */
const HISTORY_SLOTS = 30
const PERCENT_FULL = 100
const SPARK_WIDTH = 120
const SPARK_HEIGHT = 18
const ISPS: readonly { key: keyof LatencySeries; label: string }[] = [
  { key: 'telecom', label: '电信' },
  { key: 'unicom', label: '联通' },
  { key: 'mobile', label: '移动' },
]
const EMPTY = '—'
/** 离到期不足这么多天时到期天数标红。 */
const EXPIRY_WARN_DAYS = 7

interface ServerStatusCardProps {
  server: Server
  /** 列表接口给的速率与延迟近况；没有为 undefined。 */
  trend?: ServerTrend
  /** 三网延迟检测开着（列表或详情的趋势里带了 latency）。 */
  hasLatency: boolean
  /** 有 server:write，可以改账单。 */
  canEditBilling: boolean
  onEditBilling: (server: Server) => void
  /** 详情页里用：不再显示跳转详情的按钮，细节默认展开。 */
  isDetail?: boolean
  /** 标题旁的额外标签（如明文 HTTP 警告、大陆连通概况）。 */
  badges?: ReactNode
}

/** 速率小图：最近若干个 5 分钟时段的上行或下行，按最大值归一画折线。 */
function Sparkline({ values, ariaLabel }: { values: number[]; ariaLabel: string }) {
  if (values.length < 2) {
    return <svg className="spark" width={SPARK_WIDTH} height={SPARK_HEIGHT} role="img" aria-label={ariaLabel} />
  }
  const peak = Math.max(...values, 1)
  const step = SPARK_WIDTH / (values.length - 1)
  const points = values.map((v, i) => `${(i * step).toFixed(1)},${(SPARK_HEIGHT - 1 - (v / peak) * (SPARK_HEIGHT - 2)).toFixed(1)}`).join(' ')
  return (
    <svg className="spark" width={SPARK_WIDTH} height={SPARK_HEIGHT} viewBox={`0 0 ${SPARK_WIDTH} ${SPARK_HEIGHT}`} preserveAspectRatio="none" role="img" aria-label={ariaLabel}>
      <polyline points={points} fill="none" />
    </svg>
  )
}

interface MetricProps {
  icon?: ReactNode
  label: string
  value: string
  unit?: string
  sub: string
  percent: number
  tone: 'cpu' | 'mem' | 'disk'
}

/** 四宫格里的一项：名称、读数、副标题与分段条。 */
function Metric({ icon, label, value, unit, sub, percent, tone }: MetricProps) {
  return (
    <div className="server-card-metric">
      <div className="server-card-metric-head">
        <span className="server-card-label">
          {icon}
          {label}
        </span>
        <span className={`server-card-value value-${usageLevel(percent)}`}>
          {value}
          {unit !== undefined && <small>{unit}</small>}
        </span>
      </div>
      <div className="server-card-sub">{sub}</div>
      <SegmentBar percent={percent} tone={tone} ariaLabel={`${label} ${formatPercent(percent)}`} />
    </div>
  )
}

function latencyCells(points: LatencyPoint[], kind: 'latency' | 'loss'): HistoryCell[] {
  return points.map((p) => ({
    key: p.at,
    level: kind === 'latency' ? latencyLevel(p) : lossLevel(p),
    title: `${formatTime(p.at)}　${p.avgMs === null ? '全丢' : `${p.avgMs} ms`}　丢包 ${p.lossPercent}%`,
  }))
}

/** 三网延迟：每家一行，左边延迟历史、右边丢包历史，读数取最近一次。 */
function LatencyRows({ latency }: { latency: LatencySeries }) {
  const hasAny = ISPS.some((isp) => latency[isp.key].length > 0)
  if (!hasAny) {
    return <div className="server-card-sub">三网延迟还没有检测结果</div>
  }
  return (
    <div className="server-card-latency">
      {ISPS.map((isp) => {
        const points = latency[isp.key]
        const last = points.at(-1)
        let latencyText = EMPTY
        if (last !== undefined) {
          latencyText = last.avgMs === null ? '全丢' : String(last.avgMs)
        }
        return (
          <div key={isp.key} className="server-card-latency-row">
            <div className="server-card-latency-cell">
              <div className="server-card-metric-head">
                <span className="server-card-label">{isp.label}</span>
                <span className={`server-card-reading value-${latencyLevel(last)}`}>
                  {latencyText}
                  {last?.avgMs !== null && last !== undefined && <small>ms</small>}
                </span>
              </div>
              <HistoryBar cells={latencyCells(points, 'latency')} slots={HISTORY_SLOTS} ariaLabel={`${isp.label}延迟历史`} />
            </div>
            <div className="server-card-latency-cell">
              <div className="server-card-metric-head">
                <span className="server-card-label">丢包</span>
                <span className={`server-card-reading value-${lossLevel(last)}`}>
                  {last === undefined ? EMPTY : last.lossPercent}
                  {last !== undefined && <small>%</small>}
                </span>
              </div>
              <HistoryBar cells={latencyCells(points, 'loss')} slots={HISTORY_SLOTS} ariaLabel={`${isp.label}丢包历史`} />
            </div>
          </div>
        )
      })}
    </div>
  )
}

/** 流量额度：有额度时显示剩余与进度条，没有时只显示本周期已用；都带“N 天后重置”。 */
function TrafficQuota({ server, today }: { server: Server; today: string }) {
  const { traffic, billing } = server
  const resetIn = daysBetween(today, traffic.nextResetOn)
  const quota = billing.trafficQuotaBytes
  if (quota === null || quota <= 0) {
    return (
      <div className="server-card-section">
        <div className="server-card-metric-head">
          <span className="server-card-label">
            <DatabaseOutlined />
            本周期已用 {formatBytes(traffic.usedBytes)} · {resetIn} 天后重置
          </span>
          <span className="server-card-sub">未设额度</span>
        </div>
      </div>
    )
  }
  const percent = percentOf(traffic.usedBytes, quota)
  return (
    <div className="server-card-section">
      <div className="server-card-metric-head">
        <span className="server-card-label">
          <DatabaseOutlined />
          剩余 {formatBytes(Math.max(quota - traffic.usedBytes, 0))} · {resetIn} 天后重置
        </span>
        <span className="server-card-sub">
          {formatBytes(traffic.usedBytes)} / {formatBytes(quota)}
        </span>
      </div>
      <SegmentBar percent={percent} tone="quota" ariaLabel={`流量已用 ${formatPercent(percent)}`} />
    </div>
  )
}

/**
 * 服务器状态卡片（docs/05 服务器状态卡片，Ruling-83 – Ruling-86）：资源四宫格、实时速率、流量额度、三网延迟、在线与到期。
 * 服务器不在线时整卡置灰，读数是掉线前最后一份。
 */
export function ServerStatusCard({ server, trend, hasLatency, canEditBilling, onEditBilling, isDetail = false, badges }: ServerStatusCardProps) {
  const [isExpanded, setIsExpanded] = useState(isDetail)
  const navigate = useNavigate()
  const res = server.resources
  // 取一次今天即可：卡片随页面刷新重建，跨零点的误差只影响“N 天”的显示
  const [today] = useState(() => localToday(Date.now()))
  const isOnline = server.status === 'online' && !server.isDisabled
  const cpu = res?.cpu ?? 0
  const memPercent = percentOf(res?.memUsed ?? 0, res?.memTotal ?? 0)
  const diskPercent = percentOf(res?.diskUsed ?? 0, res?.diskTotal ?? 0)
  const cores = res?.cpuCores ?? 0
  const loadPercent = cores > 0 ? ((res?.load1 ?? 0) / cores) * PERCENT_FULL : 0
  const net = trend?.net ?? []
  const uptime = formatUptime(res?.uptimeSeconds ?? 0)
  const expiresIn = server.billing.expiresOn === null ? null : daysBetween(today, server.billing.expiresOn)
  const latency = trend?.latency ?? null
  const region = server.region === '' ? null : server.region

  const tags = (
    <>
      {server.publicIpv4 !== '' && <Tag className="server-card-ip" color="green">V4</Tag>}
      {server.publicIpv6 !== '' && <Tag className="server-card-ip" color="green">V6</Tag>}
      {server.host !== null && server.host.osId !== '' && <Tag>{`${server.host.osId}${server.host.arch === '' ? '' : ` · ${server.host.arch}`}`}</Tag>}
      {res !== null && res.coreState !== '' && res.coreState !== 'running' && <Tag color="red">Xray 未运行</Tag>}
      {!isOnline && <span className="server-card-sub">最近在线 {formatTime(server.lastSeenAt)}</span>}
    </>
  )
  return (
    <Card className={isOnline ? 'server-card' : 'server-card server-card-offline'}>
      <div className="server-card-header">
        {/* 详情页的页头已有名称、状态与位置，卡片不再重复 */}
        <div className="server-card-title">
          {!isDetail && region !== null && (
            <Tooltip title={countryName(region)}>
              <span className="server-card-region">{region}</span>
            </Tooltip>
          )}
          {!isDetail && <Link className="server-card-name" to={`/servers/${server.id}`}>{server.name}</Link>}
          {!isDetail && <ServerStatusTag status={server.status} lastError={server.lastError} isDisabled={server.isDisabled} />}
          {badges}
          {/* 详情页：IP、系统等标签并进标题行 */}
          {isDetail && tags}
        </div>
        <div className="server-card-actions">
          {canEditBilling && (
            <Tooltip title="账单与流量额度">
              <Button type="text" size="small" icon={<EditOutlined />} aria-label="账单与流量额度" onClick={() => onEditBilling(server)} />
            </Tooltip>
          )}
          {!isDetail && (
            <Tooltip title="详情与趋势">
              <Button type="text" size="small" icon={<LineChartOutlined />} aria-label="详情与趋势" onClick={() => void navigate(`/servers/${server.id}?tab=status`)} />
            </Tooltip>
          )}
        </div>
      </div>
      {!isDetail && <div className="server-card-tags">{tags}</div>}

      {res === null ? (
        <div className="server-card-sub server-card-section">面板还没有报过资源数据</div>
      ) : (
        <>
          <div className="server-card-grid">
            <Metric label="CPU" value={res.cpu === null ? EMPTY : cpu.toFixed(2)} unit={res.cpu === null ? undefined : '%'} sub={`${cores} 核`} percent={cpu} tone="cpu" />
            <Metric label="内存" value={memPercent.toFixed(2)} unit="%" sub={`${formatBytes(res.memUsed)} / ${formatBytes(res.memTotal)}`} percent={memPercent} tone="mem" />
            <Metric label="磁盘" value={diskPercent.toFixed(1)} unit="%" sub={`${formatBytes(res.diskUsed)} / ${formatBytes(res.diskTotal)}`} percent={diskPercent} tone="disk" />
            <Metric label="负载" value={res.load1.toFixed(2)} sub={`${res.load5.toFixed(2)} / ${res.load15.toFixed(2)}（5 / 15 分钟）`} percent={loadPercent} tone="cpu" />
          </div>
          <div className="server-card-grid server-card-section">
            <div className="server-card-metric">
              <div className="server-card-metric-head">
                <span className="server-card-label label-up">
                  <ArrowUpOutlined />
                  上行
                </span>
                <span className="server-card-rate">{formatRate(res.netUpRate)}</span>
              </div>
              <Sparkline values={net.map((p) => p.up)} ariaLabel="上行速率近况" />
              <div className="server-card-sub">本周期出站 {formatBytes(server.traffic.sentBytes)}</div>
            </div>
            <div className="server-card-metric">
              <div className="server-card-metric-head">
                <span className="server-card-label label-down">
                  <ArrowDownOutlined />
                  下行
                </span>
                <span className="server-card-rate">{formatRate(res.netDownRate)}</span>
              </div>
              <Sparkline values={net.map((p) => p.down)} ariaLabel="下行速率近况" />
              <div className="server-card-sub">本周期入站 {formatBytes(server.traffic.recvBytes)}</div>
            </div>
          </div>
        </>
      )}

      <TrafficQuota server={server} today={today} />

      {hasLatency && latency !== null && (
        <div className="server-card-section">
          <LatencyRows latency={latency} />
        </div>
      )}

      <div className="server-card-footer">
        <span className="server-card-label">
          <ClockCircleOutlined />
          在线
          <span className="server-card-reading value-info">
            {res === null ? EMPTY : uptime.value}
            {res !== null && <small>{uptime.unit}</small>}
          </span>
        </span>
        <span className="server-card-label">
          <CalendarOutlined />
          到期
          <span className={`server-card-reading ${expiresIn !== null && expiresIn <= EXPIRY_WARN_DAYS ? 'value-danger' : 'value-ok'}`}>
            {expiresIn === null ? EMPTY : expiresIn}
            {expiresIn !== null && <small>天</small>}
          </span>
        </span>
        {server.billing.priceCents !== null && (
          <Tag className="server-card-price">{formatPrice(server.billing.priceCents, server.billing.priceCurrency, server.billing.billingCycle)}</Tag>
        )}
        <span className="server-card-sub">在线用户 {server.onlineClients}</span>
      </div>

      {!isDetail && (
        <Button type="link" size="small" className="server-card-toggle" onClick={() => setIsExpanded((v) => !v)} icon={isExpanded ? <UpOutlined /> : <DownOutlined />}>
          {isExpanded ? '收起' : '更多'}
        </Button>
      )}
      {isExpanded && <ServerCardDetails server={server} />}
    </Card>
  )
}
