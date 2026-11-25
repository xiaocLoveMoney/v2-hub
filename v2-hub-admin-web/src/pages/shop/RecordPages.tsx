import { InputNumber, Select, Tag, Typography, type TableProps } from 'antd'
import { useCallback, useState } from 'react'
import { api, type LedgerEntry, type LedgerKind, type Order, type OrderKind, type Payment, type PaymentStatus, type Rebate } from '../../api/client'
import { LEDGER_KIND_OPTIONS, ORDER_KIND_OPTIONS, ledgerColumns, orderColumns, userCell } from './record-columns'
import { PagedTable } from '../../components/PagedTable'
import { useCurrency } from '../../hooks/use-currency'
import { formatMoney } from '../../money'
import { formatTime } from '../system/format'

// 交易记录的四张只读分页表（finance:read，docs/03 §5.13–§5.16）：订单、余额流水、充值单、返利。金额只做显示格式化。

/** 充值单状态的显示名与颜色；取值见 hub-api.yaml 的 PaymentStatus。 */
const PAYMENT_STATUS_META: Record<PaymentStatus, { label: string; color?: string }> = {
  pending: { label: '待支付', color: 'blue' },
  paid: { label: '已支付', color: 'green' },
  expired: { label: '已过期' },
}

interface UserIdFilterProps {
  value: number | undefined
  onChange: (value: number | undefined) => void
  label?: string
}

/** 按用户 ID 过滤的输入框（员工与代理的记录页共用）。 */
export function UserIdFilter({ value, onChange, label = '按用户 ID 过滤' }: UserIdFilterProps) {
  return (
    <InputNumber min={1} precision={0} placeholder={label} aria-label={label} className="filter-select" value={value} onChange={(v) => onChange(v === null ? undefined : v)} />
  )
}

/** 订单：只有“已支付”一种状态，不做退款与撤单（Ruling-30）。 */
export function OrdersPage() {
  const currency = useCurrency()
  const [userId, setUserId] = useState<number | undefined>(undefined)
  const [kind, setKind] = useState<OrderKind | undefined>(undefined)
  const load = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) => api.GET('/api/admin/orders', { params: { query: { page, pageSize, userId, kind } }, signal }),
    [userId, kind],
  )
  return (
    <>
      <Typography.Title level={3}>订单</Typography.Title>
      <PagedTable<Order>
        load={load}
        columns={orderColumns(currency)}
        filters={
          <>
            <UserIdFilter value={userId} onChange={setUserId} />
            <Select allowClear placeholder="按类型过滤" aria-label="按类型过滤" className="filter-select" options={ORDER_KIND_OPTIONS} value={kind} onChange={(v: OrderKind | undefined) => setKind(v)} />
          </>
        }
      />
    </>
  )
}

/** 余额流水：每次余额变动一条，带变动后的余额；管理员调整显示操作人与备注。 */
export function LedgerPage() {
  const currency = useCurrency()
  const [userId, setUserId] = useState<number | undefined>(undefined)
  const [kind, setKind] = useState<LedgerKind | undefined>(undefined)
  const load = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) => api.GET('/api/admin/wallet-ledger', { params: { query: { page, pageSize, userId, kind } }, signal }),
    [userId, kind],
  )
  return (
    <>
      <Typography.Title level={3}>余额流水</Typography.Title>
      <PagedTable<LedgerEntry>
        load={load}
        columns={ledgerColumns(currency, true)}
        filters={
          <>
            <UserIdFilter value={userId} onChange={setUserId} />
            <Select allowClear placeholder="按类型过滤" aria-label="按类型过滤" className="filter-select" options={LEDGER_KIND_OPTIONS} value={kind} onChange={(v: LedgerKind | undefined) => setKind(v)} />
          </>
        }
      />
    </>
  )
}

/** 充值单：经支付网关给余额充值的记录；没有配置网关时这里是空的（Ruling-28）。 */
export function PaymentsPage() {
  const currency = useCurrency()
  const [userId, setUserId] = useState<number | undefined>(undefined)
  const [status, setStatus] = useState<PaymentStatus | undefined>(undefined)
  const load = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) => api.GET('/api/admin/payments', { params: { query: { page, pageSize, userId, status } }, signal }),
    [userId, status],
  )
  const statusOptions = (Object.keys(PAYMENT_STATUS_META) as PaymentStatus[]).map((value) => ({ value, label: PAYMENT_STATUS_META[value].label }))
  const columns: TableProps<Payment>['columns'] = [
    { title: '创建时间', dataIndex: 'createdAt', render: (v: string) => formatTime(v) },
    { title: '充值单号', dataIndex: 'tradeNo', render: (v: string) => <span className="mono">{v}</span> },
    { title: '用户', key: 'user', render: (_: unknown, p: Payment) => userCell(p.username, p.userId) },
    { title: '支付方式', dataIndex: 'gateway' },
    { title: '金额', dataIndex: 'amount', render: (v: number) => formatMoney(v, currency) },
    { title: '状态', dataIndex: 'status', render: (s: PaymentStatus) => <Tag color={PAYMENT_STATUS_META[s]?.color}>{PAYMENT_STATUS_META[s]?.label ?? s}</Tag> },
    { title: '支付时间', dataIndex: 'paidAt', render: (v: string | null) => formatTime(v) },
  ]
  return (
    <>
      <Typography.Title level={3}>充值单</Typography.Title>
      <PagedTable<Payment>
        load={load}
        columns={columns}
        filters={
          <>
            <UserIdFilter value={userId} onChange={setUserId} />
            <Select allowClear placeholder="按状态过滤" aria-label="按状态过滤" className="filter-select" options={statusOptions} value={status} onChange={(v: PaymentStatus | undefined) => setStatus(v)} />
          </>
        }
      />
    </>
  )
}

/** 返利：被邀请人消费后记给邀请人余额的金额，只能在商店消费、不能提现（Ruling-34）。 */
export function RebatesPage() {
  const currency = useCurrency()
  const [inviterId, setInviterId] = useState<number | undefined>(undefined)
  const load = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) => api.GET('/api/admin/rebates', { params: { query: { page, pageSize, inviterId } }, signal }),
    [inviterId],
  )
  const columns: TableProps<Rebate>['columns'] = [
    { title: '时间', dataIndex: 'createdAt', render: (v: string) => formatTime(v) },
    { title: '邀请人', dataIndex: 'inviterUsername' },
    { title: '被邀请人', dataIndex: 'inviteeUsername' },
    { title: '订单号', dataIndex: 'orderNo', render: (v: string) => <span className="mono">{v}</span> },
    { title: '订单实付', dataIndex: 'orderAmount', render: (v: number) => formatMoney(v, currency) },
    { title: '比例', dataIndex: 'percent', render: (v: number) => `${v}%` },
    { title: '返利', dataIndex: 'amount', render: (v: number) => <Typography.Text strong>{formatMoney(v, currency)}</Typography.Text> },
  ]
  return (
    <>
      <Typography.Title level={3}>返利</Typography.Title>
      <PagedTable<Rebate> load={load} columns={columns} filters={<UserIdFilter value={inviterId} onChange={setInviterId} label="按邀请人 ID 过滤" />} />
    </>
  )
}
