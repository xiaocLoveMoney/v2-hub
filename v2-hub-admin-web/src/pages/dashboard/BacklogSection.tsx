import { Alert } from 'antd'
import { Link } from 'react-router'
import { api, type AnalyticsBacklog } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useRemote } from '../../hooks/use-remote'
import { ROUTES } from '../../menu'
import { formatTime } from '../system/format'
import { EMPTY_TEXT } from './analytics-format'
import { Figure, Panel, Section } from './shared'

const MINUTES_PER_HOUR = 60
const loadBacklog = (signal: AbortSignal) => api.GET('/api/admin/analytics/backlog', { signal })
const serverPath = (id: number) => ROUTES.SERVER_DETAIL.replace(':id', String(id))

/** 分钟数写成“X 小时 Y 分钟”。 */
function formatMinutes(minutes: number | null): string {
  if (minutes === null) {
    return EMPTY_TEXT
  }
  return minutes < MINUTES_PER_HOUR ? `${minutes} 分钟` : `${Math.floor(minutes / MINUTES_PER_HOUR)} 小时 ${minutes % MINUTES_PER_HOUR} 分钟`
}

/** 总览的待处理分区：工单、下发失败、面板侧问题、发件箱积压；各块按权限码出现（缺权限的接口里为 null）。 */
export function BacklogSection() {
  const state = useRemote<AnalyticsBacklog>(loadBacklog)
  const d = state.data
  if (d === null) {
    return (
      <Section>
        <ErrorAlert error={state.error} />
      </Section>
    )
  }
  const isEmpty = d.tickets === null && d.provisions === null && d.panels === null && d.outbox === null
  return (
    <Section>
      <ErrorAlert error={state.error} />
      {isEmpty && <Alert type="info" showIcon title="当前角色没有可看的待处理事项" />}
      <div className="analytics-grid">
        {d.tickets !== null && (
          <Panel title="工单" extra={<Link to={ROUTES.TICKETS}>去处理 →</Link>}>
            <div className="figure-grid">
              <Figure label="待回复" value={d.tickets.open} hint={d.tickets.oldestWaitingSince === null ? '没有在等的工单' : `最久的从 ${formatTime(d.tickets.oldestWaitingSince)} 等到现在`} />
              <Figure label="平均首次回复" value={formatMinutes(d.tickets.avgFirstReplyMinutes)} hint="近 30 日新建且已回复的工单" />
            </div>
          </Panel>
        )}
        {d.provisions !== null && (
          <Panel title="下发失败" extra={<Link to={ROUTES.USERS}>看用户 →</Link>}>
            <Figure label="失败的 client" value={d.provisions.failed} hint="这些用户在对应服务器上连不上，对账会自动重试" />
            <ul className="backlog-list">
              {d.provisions.servers.map((s) => (
                <li key={s.serverId}>
                  <Link to={serverPath(s.serverId)}>{s.name}</Link>
                  <span>{s.failed} 个</span>
                </li>
              ))}
            </ul>
          </Panel>
        )}
        {d.panels !== null && (
          <Panel title="面板" extra={<Link to={ROUTES.SERVERS}>看服务器 →</Link>}>
            <div className="figure-grid">
              <Figure label="链式下发失败" value={d.panels.chainFailed.length} hint="台服务器" />
              <Figure label="入站有漂移" value={d.panels.driftInbounds} hint="面板上被改过，待确认" />
              <Figure label="入站已不存在" value={d.panels.missingInbounds} hint="面板上已删除" />
            </div>
            <ul className="backlog-list">
              {d.panels.chainFailed.map((s) => (
                <li key={s.serverId}>
                  <Link to={serverPath(s.serverId)}>{s.name}</Link>
                  <span>{`连续失败 ${s.failCount} 次 · ${formatTime(s.lastAttemptAt)}`}</span>
                </li>
              ))}
            </ul>
          </Panel>
        )}
        {d.outbox !== null && (
          <Panel title="发件箱" extra={<Link to={ROUTES.MAIL_OUTBOX}>看邮件发件箱 →</Link>}>
            <div className="figure-grid">
              <Figure label="邮件待发" value={d.outbox.mailPending} hint={`近 24 小时失败 ${d.outbox.mailFailed24h}`} />
              <Figure label="Telegram 待发" value={d.outbox.telegramPending} hint={`近 24 小时失败 ${d.outbox.telegramFailed24h}`} />
            </div>
          </Panel>
        )}
      </div>
    </Section>
  )
}
