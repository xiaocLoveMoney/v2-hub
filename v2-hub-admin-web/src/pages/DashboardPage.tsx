import { Alert, Card, Col, Row, Statistic, Tag, Typography } from 'antd'
import type { CSSProperties } from 'react'
import { Link } from 'react-router'
import { api, type AdminDashboard } from '../api/client'
import { useSession } from '../auth/session-context'
import { ErrorAlert } from '../components/ErrorAlert'
import { useCurrency } from '../hooks/use-currency'
import { useRemote } from '../hooks/use-remote'
import { ROUTES } from '../menu'
import { formatMoney } from '../money'
import { formatTime } from './system/format'
import { formatBytes } from './users/quota'

type TrafficDay = NonNullable<AdminDashboard['traffic']>['days'][number]

const loadDashboard = (signal: AbortSignal) => api.GET('/api/admin/dashboard', { signal })
const MONTH_DAY_START = 5 // yyyy-MM-dd 里 MM-dd 的起点
const MIN_BAR_PERCENT = 2
const FULL_PERCENT = 100
const GUTTER: [number, number] = [16, 16]

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

/** 管理端总览（docs/03 §5.20）：各区块按权限码出现，缺权限的区块接口返回 null，这里不显示。 */
export function DashboardPage() {
  const { session } = useSession()
  const currency = useCurrency()
  const state = useRemote<AdminDashboard>(loadDashboard)
  const d = state.data
  const isEmpty = d !== null && d.users === null && d.servers === null && d.finance === null && d.tickets === null
  return (
    <>
      <Typography.Title level={3}>总览</Typography.Title>
      <Typography.Paragraph type="secondary">欢迎，{session?.admin.username}。</Typography.Paragraph>
      <ErrorAlert error={state.error} />
      {isEmpty && <Alert type="info" showIcon title="当前角色没有可看的统计区块" className="page-alert" />}
      {d?.servers !== null && d?.servers !== undefined && d.servers.offline.length > 0 && (
        <Alert
          type="warning"
          showIcon
          className="page-alert"
          title={`${d.servers.offline.length} 台服务器不在线`}
          description={d.servers.offline.map((s) => (
            <div key={s.id}>
              <Link to={ROUTES.SERVER_DETAIL.replace(':id', String(s.id))}>{s.name}</Link> <Tag>{s.status}</Tag>
              {s.offlineSince !== null && s.offlineSince !== undefined && `自 ${formatTime(s.offlineSince)}`}
            </div>
          ))}
        />
      )}
      <Row gutter={GUTTER}>
        {d?.users && (
          <>
            <Col xs={12} md={6}>
              <Card size="small">
                <Statistic title="用户总数" value={d.users.total} />
              </Card>
            </Col>
            <Col xs={12} md={6}>
              <Card size="small">
                <Statistic title="有效用户" value={d.users.active} />
              </Card>
            </Col>
            <Col xs={12} md={6}>
              <Card size="small">
                <Statistic title="今日 / 近 7 日新增" value={`${d.users.newToday} / ${d.users.new7d}`} />
              </Card>
            </Col>
            <Col xs={12} md={6}>
              <Card size="small">
                <Statistic title="当前在线" value={d.users.online} />
              </Card>
            </Col>
          </>
        )}
        {d?.servers && (
          <>
            <Col xs={12} md={6}>
              <Card size="small">
                <Statistic title="服务器在线" value={`${d.servers.online} / ${d.servers.total}`} />
              </Card>
            </Col>
            <Col xs={12} md={6}>
              <Card size="small">
                <Statistic title="节点数" value={d.servers.nodeCount} />
              </Card>
            </Col>
          </>
        )}
        {d?.tickets && (
          <Col xs={12} md={6}>
            <Card size="small">
              <Statistic title="待回复工单" value={d.tickets.open} />
            </Card>
          </Col>
        )}
        {d?.finance && (
          <>
            <Col xs={12} md={6}>
              <Card size="small">
                <Statistic title="今日消费 / 充值" value={`${formatMoney(d.finance.todaySpend, currency)} / ${formatMoney(d.finance.todayTopup, currency)}`} />
              </Card>
            </Col>
            <Col xs={12} md={6}>
              <Card size="small">
                <Statistic title="本月消费 / 充值" value={`${formatMoney(d.finance.monthSpend, currency)} / ${formatMoney(d.finance.monthTopup, currency)}`} />
              </Card>
            </Col>
          </>
        )}
      </Row>
      {d?.traffic && (
        <Card title={`近 7 日流量（今日 ${formatBytes(d.traffic.today)}）`} size="small" className="dashboard-traffic">
          <TrafficBars days={d.traffic.days} />
        </Card>
      )}
    </>
  )
}
