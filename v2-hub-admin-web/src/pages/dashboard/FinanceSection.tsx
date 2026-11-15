import { Table, type TableProps } from 'antd'
import { useCallback, useMemo, useState } from 'react'
import { api, type AnalyticsFinance } from '../../api/client'
import { EChart } from '../../components/EChart'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useCurrency } from '../../hooks/use-currency'
import { useRemote } from '../../hooks/use-remote'
import { formatMoney } from '../../money'
import { lineOption } from './chart-options'
import { CHART_HEIGHT, DEFAULT_DAYS, EMPTY_TEXT, formatPercent, shortDate, type AnalyticsDays } from './analytics-format'
import { BarList, DaysSwitch, Figure, Panel, Section, type BarRow } from './shared'

type PlanSale = AnalyticsFinance['plans'][number]
type CouponStat = AnalyticsFinance['coupons'][number]
type OrderKind = AnalyticsFinance['orderKinds'][number]['kind']

const DELETED_PLAN = '已删除的套餐'
const DELETED_PLAN_KEY = 'deleted'
const KIND_LABELS: Record<OrderKind, string> = { plan: '新购', renew: '续费', traffic_pack: '流量包' }

/** 总览的财务分区（finance:read）：收入趋势、订单构成、套餐销售、转化、付费用户、余额沉淀、充值单与优惠码。 */
export function FinanceSection() {
  const currency = useCurrency()
  const [days, setDays] = useState<AnalyticsDays>(DEFAULT_DAYS)
  const load = useCallback((signal: AbortSignal) => api.GET('/api/admin/analytics/finance', { params: { query: { days } }, signal }), [days])
  const state = useRemote<AnalyticsFinance>(load)
  const d = state.data
  const money = useCallback((cents: number) => formatMoney(cents, currency), [currency])

  const income = useMemo(() => {
    if (d === null) {
      return null
    }
    return lineOption({
      labels: d.days.map((x) => shortDate(x.date)),
      series: [
        { name: '订单实付', values: d.days.map((x) => x.orderPaid), color: '--hub-primary' },
        { name: '充值入账', values: d.days.map((x) => x.topup), color: '--hub-info' },
      ],
      format: money,
    })
  }, [d, money])

  if (d === null || income === null) {
    return (
      <Section toolbar={<DaysSwitch value={days} onChange={setDays} />}>
        <ErrorAlert error={state.error} />
      </Section>
    )
  }
  const kindRows: BarRow[] = d.orderKinds.map((k) => ({ key: k.kind, label: KIND_LABELS[k.kind], value: k.paid, text: `${money(k.paid)} · ${k.count} 笔` }))
  const funnelRows: BarRow[] = [
    { key: 'registered', label: '注册', value: d.funnel.registered, text: `${d.funnel.registered} 人` },
    { key: 'trial', label: '领过试用', value: d.funnel.trial, text: `${d.funnel.trial} 人 · ${formatPercent(d.funnel.trial, d.funnel.registered)}` },
    { key: 'paid', label: '付过费', value: d.funnel.paid, text: `${d.funnel.paid} 人 · ${formatPercent(d.funnel.paid, d.funnel.registered)}` },
  ]
  const orderPaid = d.days.reduce((sum, x) => sum + x.orderPaid, 0)
  const topup = d.days.reduce((sum, x) => sum + x.topup, 0)
  const settled = d.payments.paid + d.payments.expired
  const planColumns: TableProps<PlanSale>['columns'] = [
    { title: '套餐', dataIndex: 'name', render: (name: string, p: PlanSale) => (p.planId === null ? DELETED_PLAN : name) },
    { title: '笔数', dataIndex: 'count', align: 'right' },
    { title: '实付', dataIndex: 'paid', align: 'right', render: (v: number) => money(v) },
  ]
  const couponColumns: TableProps<CouponStat>['columns'] = [
    { title: '优惠码', dataIndex: 'code' },
    { title: '笔数', dataIndex: 'count', align: 'right' },
    { title: '让利', dataIndex: 'discount', align: 'right', render: (v: number) => money(v) },
    { title: '实付', dataIndex: 'paid', align: 'right', render: (v: number) => money(v) },
  ]

  return (
    <Section toolbar={<DaysSwitch value={days} onChange={setDays} />}>
      <ErrorAlert error={state.error} />
      <div className="figure-grid">
        <Figure label="付费用户" value={d.payers.users} hint={`实付合计 ${money(d.payers.paid)}`} />
        <Figure label="客单价" value={d.payers.users === 0 ? EMPTY_TEXT : money(Math.round(d.payers.paid / d.payers.users))} hint="实付合计 ÷ 付费用户" />
        <Figure label="余额沉淀" value={money(d.balanceTotal)} hint="全站用户余额合计" />
        <Figure label="充值单成功率" value={formatPercent(d.payments.paid, settled)} hint={`已支付 ${d.payments.paid} · 已过期 ${d.payments.expired} · 待支付 ${d.payments.pending}`} />
        <Figure label="试用单" value={d.trialCount} />
      </div>
      <Panel title="每日收入" extra={`订单实付 ${money(orderPaid)} · 充值入账 ${money(topup)}`}>
        <EChart option={income} height={CHART_HEIGHT} ariaLabel="每日订单实付与充值入账" />
      </Panel>
      <div className="analytics-grid">
        <Panel title="订单构成">
          <BarList rows={kindRows} />
        </Panel>
        <Panel title="注册到付费" extra="所选时间内注册的用户">
          <BarList rows={funnelRows} />
        </Panel>
        <Panel title="套餐销售" extra="新购与续费，实付最多的 10 个">
          <Table<PlanSale> size="small" rowKey={(p) => p.planId ?? DELETED_PLAN_KEY} columns={planColumns} dataSource={d.plans} pagination={false} />
        </Panel>
        <Panel title="优惠码" extra="用得最多的 5 个">
          <Table<CouponStat> size="small" rowKey="code" columns={couponColumns} dataSource={d.coupons} pagination={false} />
        </Panel>
      </div>
    </Section>
  )
}
