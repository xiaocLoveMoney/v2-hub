import {
  CheckCircleFilled,
  ClusterOutlined,
  CustomerServiceOutlined,
  PayCircleOutlined,
  ShoppingCartOutlined,
  TeamOutlined,
  WifiOutlined,
} from '@ant-design/icons'
import { Alert, Card, Spin, Tabs, Tag, Typography, type TabsProps } from 'antd'
import { lazy, Suspense, type ComponentType, type CSSProperties, type ReactNode } from 'react'
import { Link } from 'react-router'
import { api, type AdminDashboard } from '../api/client'
import { useSession } from '../auth/session-context'
import { CardPage } from '../components/CardPage'
import { ErrorAlert } from '../components/ErrorAlert'
import { useCurrency } from '../hooks/use-currency'
import { useRemote } from '../hooks/use-remote'
import { PERMISSIONS, ROUTES } from '../menu'
import { formatMoney } from '../money'
import { formatTime } from './system/format'
import { formatBytes } from './users/quota'

type TrafficDay = NonNullable<AdminDashboard['traffic']>['days'][number]
type ServerBlock = NonNullable<AdminDashboard['servers']>

const loadDashboard = (signal: AbortSignal) => api.GET('/api/admin/dashboard', { signal })
const MONTH_DAY_START = 5 // yyyy-MM-dd 里 MM-dd 的起点
const MIN_BAR_PERCENT = 2
const FULL_PERCENT = 100

// 分析分区（Ruling-66）单独成包：切到对应分区才下载代码、才请求数据，图表库不进首屏包
const UsersSection = lazy(async () => ({ default: (await import('./dashboard/UsersSection')).UsersSection }))
const TrafficSection = lazy(async () => ({ default: (await import('./dashboard/TrafficSection')).TrafficSection }))
const ServersSection = lazy(async () => ({ default: (await import('./dashboard/ServersSection')).ServersSection }))
const FinanceSection = lazy(async () => ({ default: (await import('./dashboard/FinanceSection')).FinanceSection }))
const BacklogSection = lazy(async () => ({ default: (await import('./dashboard/BacklogSection')).BacklogSection }))

const TAB_OVERVIEW = 'overview'
type TabItem = NonNullable<TabsProps['items']>[number]

/** 分析分区及其权限码；permission 为空表示任何已登录管理员可进（docs/03 §5.20 总览分析）。 */
const SECTIONS: { key: string; label: string; permission?: string; component: ComponentType }[] = [
  { key: 'users', label: '用户', permission: PERMISSIONS.USER_READ, component: UsersSection },
  { key: 'traffic', label: '流量', permission: PERMISSIONS.USER_READ, component: TrafficSection },
  { key: 'servers', label: '服务器', permission: PERMISSIONS.SERVER_READ, component: ServersSection },
  { key: 'finance', label: '财务', permission: PERMISSIONS.FINANCE_READ, component: FinanceSection },
  { key: 'backlog', label: '待处理', component: BacklogSection },
]

const sectionFallback = (
  <div className="chart-empty">
    <Spin />
  </div>
)

/** 统计卡左侧色块的配色，对应 index.css 的 .stat-tile-*。 */
type StatTone = 'primary' | 'success' | 'warning' | 'info' | 'danger'

interface StatTileProps {
  tone: StatTone
  icon: ReactNode
  label: string
  value: ReactNode
  /** 卡片底部一行补充信息。 */
  foot: ReactNode
}

/** 一张统计卡（照客户后台仪表盘）：色块图标 + 标题 + 大号数值 + 底部补充。 */
function StatTile({ tone, icon, label, value, foot }: StatTileProps) {
  return (
    <div className={`stat-tile stat-tile-${tone}`}>
      <span className="stat-tile-icon" aria-hidden="true">
        {icon}
      </span>
      <span className="stat-tile-label">{label}</span>
      <span className="stat-tile-value">{value}</span>
      <div className="stat-tile-foot">{foot}</div>
    </div>
  )
}

/** 近 7 日流量的柱状条：纯 CSS，按当周最大值折算高度。 */
function TrafficBars({ days }: { days: TrafficDay[] }) {
  const peak = Math.max(...days.map((d) => d.bytes), 1)
  return (
    <div className="traffic-bars" role="img" aria-label="近 7 日流量">
      {days.map((d) => {
        const style = { '--bar-height': `${Math.max((d.bytes / peak) * FULL_PERCENT, MIN_BAR_PERCENT)}%` } as CSSProperties
        return (
          <div key={d.date} className="traffic-bar" title={`${d.date}：${formatBytes(d.bytes)}`}>
            <div className="traffic-bar-fill" style={style} />
            <span>{d.date.slice(MONTH_DAY_START)}</span>
          </div>
        )
      })}
    </div>
  )
}

/** 服务器状态卡：列出不在线的服务器；全部在线时给一句确认。 */
function ServerStatusCard({ servers }: { servers: ServerBlock }) {
  const offline = servers.offline
  if (offline.length === 0) {
    return (
      <Card title="服务器状态">
        <div className="dashboard-all-online">
          <CheckCircleFilled className="dashboard-ok-icon" />
          <span>全部 {servers.total} 台服务器在线</span>
        </div>
      </Card>
    )
  }
  return (
    <Card title="服务器状态">
      <Alert type="warning" showIcon title={`${offline.length} 台服务器不在线`} className="page-alert" />
      <ul className="dashboard-offline-list">
        {offline.map((s) => (
          <li key={s.id}>
            <Link to={ROUTES.SERVER_DETAIL.replace(':id', String(s.id))}>{s.name}</Link>
            <Tag>{s.status}</Tag>
            {s.offlineSince !== null && s.offlineSince !== undefined && (
              <Typography.Text type="secondary">自 {formatTime(s.offlineSince)}</Typography.Text>
            )}
          </li>
        ))}
      </ul>
    </Card>
  )
}

/**
 * 管理端总览（docs/03 §5.20）：“概览”各区块按权限码出现，缺权限的区块接口返回 null，这里不显示；
 * 其余分区（用户 / 流量 / 服务器 / 财务 / 待处理）按会话里的权限码出现，切到时才加载（Ruling-66）。
 */
export function DashboardPage() {
  const { session, can } = useSession()
  const currency = useCurrency()
  const state = useRemote<AdminDashboard>(loadDashboard)
  const d = state.data
  const isEmpty = d !== null && d.users === null && d.servers === null && d.finance === null && d.tickets === null && d.traffic === null
  const money = (cents: number) => formatMoney(cents, currency)
  const greeting = <Typography.Text type="secondary">欢迎，{session?.admin.username}</Typography.Text>

  const overview = (
    <>
      {isEmpty && <Alert type="info" showIcon title="当前角色没有可看的统计区块" className="page-alert" />}
      <div className="stat-grid">
        {d?.users && (
          <>
            <StatTile tone="primary" icon={<TeamOutlined />} label="用户总数" value={d.users.total} foot={`有效用户 ${d.users.active}`} />
            <StatTile
              tone="success"
              icon={<WifiOutlined />}
              label="当前在线"
              value={d.users.online}
              foot={`今日新增 ${d.users.newToday} · 近 7 日 ${d.users.new7d}`}
            />
          </>
        )}
        {d?.servers && (
          <StatTile
            tone={d.servers.offline.length > 0 ? 'warning' : 'info'}
            icon={<ClusterOutlined />}
            label="服务器在线"
            value={`${d.servers.online} / ${d.servers.total}`}
            foot={<Link to={ROUTES.SERVERS}>节点 {d.servers.nodeCount} 个 · 查看服务器 →</Link>}
          />
        )}
        {d?.tickets && (
          <StatTile
            tone="warning"
            icon={<CustomerServiceOutlined />}
            label="待回复工单"
            value={d.tickets.open}
            foot={<Link to={ROUTES.TICKETS}>去处理 →</Link>}
          />
        )}
        {d?.finance && (
          <>
            <StatTile tone="danger" icon={<ShoppingCartOutlined />} label="今日消费" value={money(d.finance.todaySpend)} foot={`今日充值 ${money(d.finance.todayTopup)}`} />
            <StatTile tone="primary" icon={<PayCircleOutlined />} label="本月消费" value={money(d.finance.monthSpend)} foot={`本月充值 ${money(d.finance.monthTopup)}`} />
          </>
        )}
      </div>
      {(d?.traffic || d?.servers) && (
        <div className="dashboard-split">
          {d.traffic && (
            <Card title="近 7 日流量" extra={<Typography.Text type="secondary">今日 {formatBytes(d.traffic.today)}</Typography.Text>}>
              <TrafficBars days={d.traffic.days} />
            </Card>
          )}
          {d.servers && <ServerStatusCard servers={d.servers} />}
        </div>
      )}
    </>
  )
  const sections: TabItem[] = SECTIONS.filter((sec) => sec.permission === undefined || can(sec.permission)).map((sec) => ({
    key: sec.key,
    label: sec.label,
    children: (
      <Suspense fallback={sectionFallback}>
        <sec.component />
      </Suspense>
    ),
  }))

  return (
    <CardPage title="总览" extra={greeting}>
      <ErrorAlert error={state.error} />
      <Tabs className="dashboard-tabs" defaultActiveKey={TAB_OVERVIEW} items={[{ key: TAB_OVERVIEW, label: '概览', children: overview }, ...sections]} />
    </CardPage>
  )
}
