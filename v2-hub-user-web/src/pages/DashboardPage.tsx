import { CalendarClock, Gauge, MonitorSmartphone, Server, Wallet } from 'lucide-react'
import { useState } from 'react'
import { Link } from 'react-router'
import { api, type Dashboard, type TrafficPoint } from '../api/client'
import { Donut, HourBars } from '../components/charts'
import { Alert, Card, Loading, PageHeader, StatCard } from '../components/ui'
import { daysLeft, formatBytes, formatDate, formatTime, usedPercent } from '../format'
import { useRemote } from '../hooks/use-remote'
import { ROUTES } from '../menu'
import { formatMoney } from '../money'
import { plural, useI18n, type Translate } from '../i18n/i18n-context'
import { useCurrency, useSite } from '../site/site-context'
import { LatestAnnouncementCard, PopupAnnouncement } from './AnnouncementCards'
import { CheckinCard } from './CheckinCard'
import { SubscriptionCard } from './SubscriptionCard'

const TRAFFIC_HOURS = 72
const ICON_SIZE = 26
/** 剩余流量低于额度的这个比例时提示（docs/03 §5.5 用户状态提示）。 */
const LOW_TRAFFIC_RATIO = 0.1
const STATUS_ACTIVE = 'active'

const loadDashboard = (signal: AbortSignal) => api.GET('/api/user/dashboard', { signal })
const loadTraffic = (signal: AbortSignal) => api.GET('/api/user/traffic', { params: { query: { hours: TRAFFIC_HOURS } }, signal })

function StatusBanner({ d }: { d: Dashboard }) {
  const { t } = useI18n()
  if (d.status === 'expired') {
    return <Alert tone="danger">{t('dash.expired')}</Alert>
  }
  if (d.status === 'limited') {
    return <Alert tone="danger">{t('dash.limited')}</Alert>
  }
  const left = d.trafficLimit - d.trafficUsed
  if (d.status === STATUS_ACTIVE && d.trafficLimit > 0 && left < d.trafficLimit * LOW_TRAFFIC_RATIO) {
    return <Alert tone="warning">{t('dash.lowTraffic', { amount: formatBytes(Math.max(left, 0)) })}</Alert>
  }
  return null
}

function expiryStat(t: Translate, tag: string, d: Dashboard, now: number): { value: string; foot: string } {
  const plan = d.planName || t('dash.noPlan')
  if (d.expiresAt !== null && d.expiresAt !== undefined) {
    return { value: plural(t, daysLeft(d.expiresAt, now), 'dash.daysOne', 'dash.daysMany'), foot: t('dash.expiresOn', { plan, date: formatDate(d.expiresAt, tag) }) }
  }
  if (d.pendingDays > 0) {
    return { value: plural(t, d.pendingDays, 'dash.daysOne', 'dash.daysMany'), foot: t('dash.fromFirstUse', { plan: d.planName }) }
  }
  return { value: t('common.unlimited'), foot: plan }
}

/** 仪表盘（docs/06 §5）：四张统计卡 + 7:5 双栏（公告、72 小时用量与快速导入 / 流量环形图与签到）；有弹窗公告时进入即弹。 */
export function DashboardPage() {
  const { t, tag } = useI18n()
  const site = useSite()
  const currency = useCurrency()
  const dashboard = useRemote<Dashboard>(loadDashboard)
  const traffic = useRemote<TrafficPoint[]>(loadTraffic)
  // 进入页面时取一次当前时刻，渲染期间保持不变
  const [now] = useState(() => Date.now())
  const d = dashboard.data

  if (dashboard.error !== null) {
    return (
      <>
        <PageHeader title={t('menu.dashboard')} />
        <Alert tone="plain">{dashboard.error.message}</Alert>
      </>
    )
  }
  if (d === null) {
    return <Loading />
  }

  const hasPlan = d.planName !== ''
  const expiry = expiryStat(t, tag, d, now)
  const left = Math.max(d.trafficLimit - d.trafficUsed, 0)
  const usedBefore = Math.max(d.trafficUsed - d.trafficToday, 0)

  return (
    <>
      <PageHeader title={t('menu.dashboard')} />
      <StatusBanner d={d} />
      {!hasPlan && (
        <Card title={t('dash.noPlanTitle')}>
          {site.shopEnabled === true ? (
            <p>
              {t('dash.noPlanShopBefore')}
              <Link to={ROUTES.SHOP}>{t('menu.shop')}</Link>
              {t('dash.noPlanShopAfter')}
            </p>
          ) : (
            <p>{t('dash.noPlanAdmin')}</p>
          )}
        </Card>
      )}
      <div className="hub-stat-grid">
        <StatCard icon={<CalendarClock size={ICON_SIZE} />} tone="primary" label={t('dash.planLeft')} value={expiry.value} foot={expiry.foot} />
        <StatCard
          icon={<Gauge size={ICON_SIZE} />}
          tone="success"
          label={t('dash.trafficLeft')}
          value={d.trafficLimit === 0 ? t('common.unlimited') : formatBytes(left)}
          foot={`${t('dash.todayUsed', { amount: formatBytes(d.trafficToday) })}${d.trafficLimit === 0 ? '' : t('dash.usedPercent', { percent: usedPercent(d.trafficUsed, d.trafficLimit) })}`}
        />
        <StatCard
          icon={<MonitorSmartphone size={ICON_SIZE} />}
          tone="warning"
          label={t('dash.onlineDevices')}
          value={d.onlineDevices === null || d.onlineDevices === undefined ? t('common.dash') : `${d.onlineDevices}${d.limitIp > 0 ? ` / ${d.limitIp}` : ''}`}
          foot={t('dash.lastUsed', { time: formatTime(d.lastUsedAt, tag) })}
        />
        {site.shopEnabled === true ? (
          <StatCard
            icon={<Wallet size={ICON_SIZE} />}
            tone="info"
            label={t('dash.balance')}
            value={formatMoney(d.balance ?? 0, currency, tag)}
            foot={
              <>
                <Link to={ROUTES.WALLET}>{t('dash.topup')}</Link> · <Link to={ROUTES.NODES}>{plural(t, d.nodeCount, 'dash.nodesLinkOne', 'dash.nodesLinkMany')}</Link>
              </>
            }
          />
        ) : (
          <StatCard
            icon={<Server size={ICON_SIZE} />}
            tone="info"
            label={t('dash.nodes')}
            value={t('dash.nodeCount', { n: d.nodeCount })}
            foot={<Link to={ROUTES.NODES}>{t('dash.viewNodes')}</Link>}
          />
        )}
      </div>
      <div className="hub-grid-7-5">
        <div>
          {d.announcement !== null && d.announcement !== undefined && <LatestAnnouncementCard announcement={d.announcement} />}
          <Card title={t('dash.recentUsage', { hours: TRAFFIC_HOURS })}>
            {traffic.error !== null && <Alert tone="plain">{traffic.error.message}</Alert>}
            {traffic.data !== null && traffic.data.length === 0 && <div className="hub-empty">{t('dash.noUsage')}</div>}
            {traffic.data !== null && traffic.data.length > 0 && <HourBars points={traffic.data} hours={TRAFFIC_HOURS} now={now} />}
          </Card>
          {hasPlan && <SubscriptionCard />}
        </div>
        <div>
          <Card title={t('dash.trafficUsage')}>
            <Donut
              centerTitle={t(d.trafficLimit === 0 ? 'dash.used' : 'dash.left')}
              centerValue={d.trafficLimit === 0 ? formatBytes(d.trafficUsed) : formatBytes(left)}
              segments={[
                { label: t('dash.usedBefore'), value: usedBefore, color: 'var(--hub-primary-light)' },
                { label: t('dash.usedToday'), value: d.trafficToday, color: 'var(--hub-primary-glow)' },
                ...(d.trafficLimit === 0 ? [] : [{ label: t('dash.left'), value: left, color: 'var(--hub-primary)' }]),
              ]}
            />
          </Card>
          {site.checkinEnabled && <CheckinCard onRewarded={dashboard.reload} />}
        </div>
      </div>
      {d.popupAnnouncement !== null && d.popupAnnouncement !== undefined && (
        <PopupAnnouncement key={d.popupAnnouncement.id} announcement={d.popupAnnouncement} />
      )}
    </>
  )
}
