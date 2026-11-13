import { DownloadOutlined } from '@ant-design/icons'
import { App, Button, Card, Segmented, Space, Statistic, Table, Typography, type TableProps } from 'antd'
import { useCallback, useState } from 'react'
import { api, type FinanceGranularity, type FinanceStats } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useCurrency } from '../../hooks/use-currency'
import { useRemote } from '../../hooks/use-remote'
import { formatMoney } from '../../money'
import { TABLE_SCROLL } from '../system/format'

type DayRow = FinanceStats['days'][number]

/** 可选的统计区间；上限与 hub-api.yaml 的 days（≤ 366）、months（≤ 24）一致。 */
const DAY_OPTIONS = [
  { value: 7, label: '近 7 天' },
  { value: 30, label: '近 30 天' },
  { value: 90, label: '近 90 天' },
  { value: 366, label: '近一年' },
]
const MONTH_OPTIONS = [
  { value: 6, label: '近 6 个月' },
  { value: 12, label: '近 12 个月' },
  { value: 24, label: '近 24 个月' },
]
const GRANULARITY_OPTIONS: { value: FinanceGranularity; label: string }[] = [
  { value: 'day', label: '按日' },
  { value: 'month', label: '按月' },
]
const DEFAULT_DAYS = 30
const DEFAULT_MONTHS = 12
const MONTH_LABEL_LEN = 7 // yyyy-MM
const EXPORT_PATH = '/api/admin/finance/stats.csv'
const FILENAME_PATTERN = /filename=([^;]+)/
const FALLBACK_FILENAME = 'finance.csv'
const EXPORT_FAILED = '导出失败，请稍后重试'

/** 收支统计（finance:read，docs/03 §5.20 财务报表）：按日或按月列出充值、消费与返利，可导出 CSV。 */
export function FinanceStatsPage() {
  const currency = useCurrency()
  const { message } = App.useApp()
  const [granularity, setGranularity] = useState<FinanceGranularity>('day')
  const [days, setDays] = useState(DEFAULT_DAYS)
  const [months, setMonths] = useState(DEFAULT_MONTHS)
  const [isExporting, setIsExporting] = useState(false)
  const isMonthly = granularity === 'month'
  const query = isMonthly ? { granularity, months } : { granularity, days }
  const load = useCallback(
    (signal: AbortSignal) => api.GET('/api/admin/finance/stats', { params: { query: isMonthly ? { granularity, months } : { granularity, days } }, signal }),
    [granularity, days, months, isMonthly],
  )
  const stats = useRemote<FinanceStats>(load)
  const totals = stats.data?.totals
  // 接口按日期升序给，表格里新的在前更顺手；toReversed 产生新数组，不动接口返回的那份
  const rows = (stats.data?.days ?? []).toReversed()

  // CSV 不走响应信封，用同源 fetch 取回后交给浏览器下载
  const exportCSV = async () => {
    setIsExporting(true)
    try {
      const target = new URL(EXPORT_PATH, window.location.origin)
      for (const [k, v] of Object.entries(query)) {
        target.searchParams.set(k, String(v))
      }
      const res = await fetch(new Request(target, { credentials: 'same-origin' }))
      if (!res.ok) {
        void message.error(EXPORT_FAILED)
        return
      }
      const blob = await res.blob()
      const name = FILENAME_PATTERN.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ?? FALLBACK_FILENAME
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = name
      a.click()
      URL.revokeObjectURL(url)
    } catch (err) {
      reportError(err)
      void message.error(EXPORT_FAILED)
    } finally {
      setIsExporting(false)
    }
  }

  const columns: TableProps<DayRow>['columns'] = [
    { title: isMonthly ? '月份' : '日期', dataIndex: 'date', render: (v: string) => (isMonthly ? v.slice(0, MONTH_LABEL_LEN) : v) },
    { title: '充值入账', dataIndex: 'topup', render: (v: number) => formatMoney(v, currency) },
    { title: '消费', dataIndex: 'spend', render: (v: number) => formatMoney(v, currency) },
    { title: '订单数', dataIndex: 'orderCount' },
    { title: '返利', dataIndex: 'rebate', render: (v: number) => formatMoney(v, currency) },
  ]

  return (
    <>
      <Space className="page-header" wrap>
        <Typography.Title level={3}>收支统计</Typography.Title>
        <Space wrap>
          <Segmented options={GRANULARITY_OPTIONS} value={granularity} onChange={(v) => setGranularity(v as FinanceGranularity)} />
          {isMonthly ? (
            <Segmented options={MONTH_OPTIONS} value={months} onChange={(v) => setMonths(Number(v))} />
          ) : (
            <Segmented options={DAY_OPTIONS} value={days} onChange={(v) => setDays(Number(v))} />
          )}
          <Button icon={<DownloadOutlined />} loading={isExporting} onClick={() => void exportCSV()}>
            导出 CSV
          </Button>
        </Space>
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
