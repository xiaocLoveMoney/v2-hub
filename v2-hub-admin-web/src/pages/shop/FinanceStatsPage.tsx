import { Card, Segmented, Space, Statistic, Table, Typography, type TableProps } from 'antd'
import { useCallback, useState } from 'react'
import { api, type FinanceStats } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useCurrency } from '../../hooks/use-currency'
import { useRemote } from '../../hooks/use-remote'
import { formatMoney } from '../../money'
import { TABLE_SCROLL } from '../system/format'

type DayRow = FinanceStats['days'][number]

/** 可选的统计区间（天）；上限与 hub-api.yaml 的 days 参数一致。 */
const RANGE_OPTIONS = [
  { value: 7, label: '近 7 天' },
  { value: 30, label: '近 30 天' },
  { value: 90, label: '近 90 天' },
]
const DEFAULT_RANGE = 30

/** 收支统计（finance:read）：按平台时区的自然日列出充值、消费与返利，上方是区间合计与当前余额总和。 */
export function FinanceStatsPage() {
  const currency = useCurrency()
  const [days, setDays] = useState(DEFAULT_RANGE)
  const load = useCallback((signal: AbortSignal) => api.GET('/api/admin/finance/stats', { params: { query: { days } }, signal }), [days])
  const stats = useRemote<FinanceStats>(load)
  const totals = stats.data?.totals
  // 接口按日期升序给，表格里新的在前更顺手；toReversed 产生新数组，不动接口返回的那份
  const rows = (stats.data?.days ?? []).toReversed()

  const columns: TableProps<DayRow>['columns'] = [
    { title: '日期', dataIndex: 'date' },
    { title: '充值入账', dataIndex: 'topup', render: (v: number) => formatMoney(v, currency) },
    { title: '消费', dataIndex: 'spend', render: (v: number) => formatMoney(v, currency) },
    { title: '订单数', dataIndex: 'orderCount' },
    { title: '返利', dataIndex: 'rebate', render: (v: number) => formatMoney(v, currency) },
  ]

  return (
    <>
      <Space className="page-header">
        <Typography.Title level={3}>收支统计</Typography.Title>
        <Segmented options={RANGE_OPTIONS} value={days} onChange={(v) => setDays(Number(v))} />
      </Space>
      <ErrorAlert error={stats.error} />
      <Space wrap size="middle" className="page-filters">
        <Card size="small">
          <Statistic title="充值入账（区间）" value={formatMoney(totals?.topup ?? 0, currency)} />
        </Card>
        <Card size="small">
          <Statistic title="消费（区间）" value={formatMoney(totals?.spend ?? 0, currency)} />
        </Card>
        <Card size="small">
          <Statistic title="返利（区间）" value={formatMoney(totals?.rebate ?? 0, currency)} />
        </Card>
        <Card size="small">
          <Statistic title="当前用户余额总和" value={formatMoney(totals?.balance ?? 0, currency)} />
        </Card>
      </Space>
      <Table<DayRow> rowKey="date" scroll={TABLE_SCROLL} loading={stats.isLoading} columns={columns} dataSource={rows} pagination={false} />
    </>
  )
}
