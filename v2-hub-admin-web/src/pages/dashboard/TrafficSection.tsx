import { useCallback, useMemo, useState } from 'react'
import { api, type AnalyticsTraffic } from '../../api/client'
import { EChart } from '../../components/EChart'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useRemote } from '../../hooks/use-remote'
import { formatBytes } from '../users/quota'
import { barOption } from './chart-options'
import { CHART_HEIGHT, DEFAULT_DAYS, shortDate, type AnalyticsDays } from './analytics-format'
import { BarList, DaysSwitch, Panel, Section, type BarRow } from './shared'

const HOUR_PAD = 2

/** 总览的流量分区（user:read）：每日上下行、一天里的时段分布、按服务器的流量（另需 server:read）。 */
export function TrafficSection() {
  const [days, setDays] = useState<AnalyticsDays>(DEFAULT_DAYS)
  const load = useCallback((signal: AbortSignal) => api.GET('/api/admin/analytics/traffic', { params: { query: { days } }, signal }), [days])
  const state = useRemote<AnalyticsTraffic>(load)
  const d = state.data

  const charts = useMemo(() => {
    if (d === null) {
      return null
    }
    return {
      daily: barOption(
        {
          labels: d.days.map((x) => shortDate(x.date)),
          series: [
            { name: '下行', values: d.days.map((x) => x.down), color: '--hub-primary' },
            { name: '上行', values: d.days.map((x) => x.up), color: '--hub-info' },
          ],
          format: formatBytes,
        },
        true,
      ),
      hourly: barOption({
        labels: d.hours.map((h) => `${String(h.hour).padStart(HOUR_PAD, '0')} 时`),
        series: [{ name: '日均流量', values: d.hours.map((h) => h.bytes), color: '--hub-primary' }],
        format: formatBytes,
      }),
    }
  }, [d])

  if (d === null || charts === null) {
    return (
      <Section toolbar={<DaysSwitch value={days} onChange={setDays} />}>
        <ErrorAlert error={state.error} />
      </Section>
    )
  }
  const up = d.days.reduce((sum, x) => sum + x.up, 0)
  const down = d.days.reduce((sum, x) => sum + x.down, 0)
  const serverRows: BarRow[] = (d.servers ?? []).map((s) => ({ key: String(s.serverId), label: s.name, value: s.up + s.down, text: formatBytes(s.up + s.down) }))

  return (
    <Section toolbar={<DaysSwitch value={days} onChange={setDays} />}>
      <ErrorAlert error={state.error} />
      <Panel title="每日流量" extra={`下行 ${formatBytes(down)} · 上行 ${formatBytes(up)}`}>
        <EChart option={charts.daily} height={CHART_HEIGHT} ariaLabel="每日上行与下行流量" />
      </Panel>
      <div className="analytics-grid">
        <Panel title="时段分布" extra="一天里每个小时的日均流量">
          <EChart option={charts.hourly} height={CHART_HEIGHT} ariaLabel="一天里各小时的日均流量" />
        </Panel>
        {d.servers !== null && (
          <Panel title="按服务器" extra="只有本功能上线之后的数据">
            <BarList rows={serverRows} emptyText="这段时间还没有按服务器的流量记录" />
          </Panel>
        )}
      </div>
    </Section>
  )
}
