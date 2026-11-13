import { CalendarClock, Gauge, MonitorSmartphone, Server } from 'lucide-react'
import { useState } from 'react'
import { Link } from 'react-router'
import { api, type Dashboard, type TrafficPoint } from '../api/client'
import { Donut, HourBars } from '../components/charts'
import { Alert, Card, PageHeader, StatCard } from '../components/ui'
import { daysLeft, formatBytes, formatDate, formatTime, usedPercent } from '../format'
import { useRemote } from '../hooks/use-remote'
import { ROUTES } from '../menu'
import { SubscriptionCard } from './SubscriptionCard'

const TRAFFIC_HOURS = 72
const ICON_SIZE = 26
/** 剩余流量低于额度的这个比例时提示（docs/03 §5.5 用户状态提示）。 */
const LOW_TRAFFIC_RATIO = 0.1
const STATUS_ACTIVE = 'active'

const loadDashboard = (signal: AbortSignal) => api.GET('/api/user/dashboard', { signal })
const loadTraffic = (signal: AbortSignal) => api.GET('/api/user/traffic', { params: { query: { hours: TRAFFIC_HOURS } }, signal })

function StatusBanner({ d }: { d: Dashboard }) {
  if (d.status === 'expired') {
    return <Alert tone="danger">套餐已到期，节点已停用。请联系管理员续费。</Alert>
  }
  if (d.status === 'limited') {
    return <Alert tone="danger">流量已用尽，节点已停用。请联系管理员重置或升级套餐。</Alert>
  }
  const left = d.trafficLimit - d.trafficUsed
  if (d.status === STATUS_ACTIVE && d.trafficLimit > 0 && left < d.trafficLimit * LOW_TRAFFIC_RATIO) {
    return <Alert tone="warning">剩余流量不足 10%（{formatBytes(Math.max(left, 0))}），请留意用量。</Alert>
  }
  return null
}

function expiryStat(d: Dashboard, now: number): { value: string; foot: string } {
  if (d.expiresAt !== null && d.expiresAt !== undefined) {
    return { value: `${daysLeft(d.expiresAt, now)} 天`, foot: `${d.planName || '未分配套餐'} · ${formatDate(d.expiresAt)} 到期` }
  }
  if (d.pendingDays > 0) {
    return { value: `${d.pendingDays} 天`, foot: `${d.planName} · 首次使用后开始计时` }
  }
  return { value: '不限', foot: d.planName || '未分配套餐' }
}

/** 仪表盘（docs/06 §5）：四张统计卡 + 7:5 双栏（72 小时用量 / 流量环形图与快速导入）。 */
export function DashboardPage() {
  const dashboard = useRemote<Dashboard>(loadDashboard)
  const traffic = useRemote<TrafficPoint[]>(loadTraffic)
  // 进入页面时取一次当前时刻，渲染期间保持不变
  const [now] = useState(() => Date.now())
  const d = dashboard.data

  if (dashboard.error !== null) {
    return (
      <>
        <PageHeader title="仪表盘" />
        <Alert tone="plain">{dashboard.error.message}</Alert>
      </>
    )
  }
  if (d === null) {
    return <div className="hub-empty">加载中…</div>
  }

  const hasPlan = d.planName !== ''
  const expiry = expiryStat(d, now)
  const left = Math.max(d.trafficLimit - d.trafficUsed, 0)
  const usedBefore = Math.max(d.trafficUsed - d.trafficToday, 0)

  return (
    <>
      <PageHeader title="仪表盘" />
      <StatusBanner d={d} />
      {!hasPlan && (
        <Card title="👋 还没有可用套餐">
          <p>账号已创建，但还没有分配套餐，暂时没有可用节点。请联系管理员开通，开通后回到这里导入订阅即可使用。</p>
        </Card>
      )}
      <div className="hub-stat-grid">
        <StatCard icon={<CalendarClock size={ICON_SIZE} />} tone="primary" label="套餐剩余" value={expiry.value} foot={expiry.foot} />
        <StatCard
          icon={<Gauge size={ICON_SIZE} />}
          tone="success"
          label="剩余流量"
          value={d.trafficLimit === 0 ? '不限' : formatBytes(left)}
          foot={`今日已用 ${formatBytes(d.trafficToday)}${d.trafficLimit === 0 ? '' : ` · 已用 ${usedPercent(d.trafficUsed, d.trafficLimit)}%`}`}
        />
        <StatCard
          icon={<MonitorSmartphone size={ICON_SIZE} />}
          tone="warning"
          label="在线设备"
          value={d.onlineDevices === null || d.onlineDevices === undefined ? '—' : `${d.onlineDevices}${d.limitIp > 0 ? ` / ${d.limitIp}` : ''}`}
          foot={`上次使用 ${formatTime(d.lastUsedAt)}`}
        />
        <StatCard
          icon={<Server size={ICON_SIZE} />}
          tone="info"
          label="可用节点"
          value={`${d.nodeCount} 个`}
          foot={<Link to={ROUTES.NODES}>查看节点列表 →</Link>}
        />
      </div>
      <div className="hub-grid-7-5">
        <div>
          <Card title={`最近 ${TRAFFIC_HOURS} 小时用量`}>
            {traffic.error !== null && <Alert tone="plain">{traffic.error.message}</Alert>}
            {traffic.data !== null && traffic.data.length === 0 && <div className="hub-empty">这段时间没有用量</div>}
            {traffic.data !== null && traffic.data.length > 0 && <HourBars points={traffic.data} hours={TRAFFIC_HOURS} now={now} />}
          </Card>
          {hasPlan && <SubscriptionCard />}
        </div>
        <Card title="流量使用">
          <Donut
            centerTitle={d.trafficLimit === 0 ? '已用' : '剩余'}
            centerValue={d.trafficLimit === 0 ? formatBytes(d.trafficUsed) : formatBytes(left)}
            segments={[
              { label: '此前已用', value: usedBefore, color: 'var(--hub-primary-light)' },
              { label: '今日已用', value: d.trafficToday, color: 'var(--hub-primary-glow)' },
              ...(d.trafficLimit === 0 ? [] : [{ label: '剩余', value: left, color: 'var(--hub-primary)' }]),
            ]}
          />
        </Card>
      </div>
    </>
  )
}
