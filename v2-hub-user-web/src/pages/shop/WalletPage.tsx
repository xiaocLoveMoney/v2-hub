import { QRCodeSVG } from 'qrcode.react'
import { useCallback, useState, type FormEvent } from 'react'
import { Link } from 'react-router'
import { api, csrfHeader, toApiError, type LedgerEntry, type Order, type PageData, type Wallet } from '../../api/client'
import { useToast } from '../../components/toast-context'
import { Alert, Badge, Button, Card, Empty, Field, Modal, PageHeader, Pager } from '../../components/ui'
import { formatBytes, formatTime } from '../../format'
import { mutate, useRemote } from '../../hooks/use-remote'
import { ROUTES } from '../../menu'
import { formatMoney, parseAmountToCents } from '../../money'
import { useCurrency, useSite } from '../../site/site-context'
import { LEDGER_KIND_LABELS, durationLabel, orderKindLabel } from './labels'

const PAGE_SIZE = 10
const MAX_CODE_INPUT_LEN = 24
const MIN_CODE_INPUT_LEN = 16
const QR_SIZE = 200
/** 在线充值的最低金额（分），与 hub-api.yaml 的 PaymentCreateInput 一致。 */
const MIN_TOPUP_CENTS = 100
const HTTPS_PREFIX = 'https://'

const loadWallet = (signal: AbortSignal) => api.GET('/api/user/wallet', { signal })

/** 兑换充值码：成功后余额立即到账（docs/03 §5.15）。 */
function RedeemCard({ onRedeemed }: { onRedeemed: () => void }) {
  const toast = useToast()
  const currency = useCurrency()
  const [code, setCode] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    setIsBusy(true)
    setError(null)
    try {
      const res = await api.POST('/api/user/wallet/redeem', { params: csrfHeader(), body: { code: code.trim() } })
      if (res.error !== undefined || res.data?.data === undefined) {
        setError(toApiError(res.error).message)
        return
      }
      setCode('')
      toast.show(`充值成功，到账 ${formatMoney(res.data.data.amount, currency)}`, 'success')
      onRedeemed()
    } catch (err) {
      reportError(err)
      setError(toApiError(err).message)
    } finally {
      setIsBusy(false)
    }
  }

  return (
    <Card title="兑换充值码">
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <form onSubmit={(e) => void handleSubmit(e)} noValidate>
        <Field label="充值码" autoComplete="off" maxLength={MAX_CODE_INPUT_LEN} hint="16 位，可以带空格或连字符" value={code} onChange={(e) => setCode(e.target.value)} />
        <Button type="submit" variant="primary" isBusy={isBusy} disabled={code.trim().length < MIN_CODE_INPUT_LEN}>
          兑换
        </Button>
      </form>
    </Card>
  )
}

/** 在线充值：站点配了支付网关才出现；网关给跳转地址就跳转，给二维码内容就展示二维码（docs/03 §5.15）。 */
function OnlineTopupCard({ gateways }: { gateways: Wallet['gateways'] }) {
  const [gateway, setGateway] = useState(gateways[0]?.code ?? '')
  const [amount, setAmount] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)
  const [qrContent, setQrContent] = useState<string | null>(null)
  const cents = parseAmountToCents(amount)

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (cents === null) {
      return
    }
    setIsBusy(true)
    setError(null)
    try {
      const res = await api.POST('/api/user/wallet/payments', { params: csrfHeader(), body: { gateway, amount: cents } })
      if (res.error !== undefined || res.data?.data === undefined) {
        setError(toApiError(res.error).message)
        return
      }
      const { payUrl, qrContent: qr } = res.data.data
      // 只跟随 https 的支付地址，其余一律不跳
      if (payUrl.startsWith(HTTPS_PREFIX)) {
        window.location.assign(payUrl)
        return
      }
      if (qr !== '') {
        setQrContent(qr)
        return
      }
      setError('支付方式没有返回可用的支付入口，请换一种方式')
    } catch (err) {
      reportError(err)
      setError(toApiError(err).message)
    } finally {
      setIsBusy(false)
    }
  }

  return (
    <Card title="在线充值">
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <form onSubmit={(e) => void handleSubmit(e)} noValidate>
        <div className="hub-seg hub-row-gap" role="radiogroup" aria-label="支付方式">
          {gateways.map((g) => (
            <button key={g.code} type="button" role="radio" aria-checked={g.code === gateway} className={`hub-seg-item${g.code === gateway ? ' is-active' : ''}`} onClick={() => setGateway(g.code)}>
              {g.name}
            </button>
          ))}
        </div>
        <Field label="充值金额" inputMode="decimal" autoComplete="off" hint="最低 1 元，支付成功后自动到账" value={amount} onChange={(e) => setAmount(e.target.value)} />
        <Button type="submit" variant="primary" isBusy={isBusy} disabled={cents === null || cents < MIN_TOPUP_CENTS || gateway === ''}>
          去支付
        </Button>
      </form>
      <Modal title="扫码支付" isOpen={qrContent !== null} onClose={() => setQrContent(null)}>
        <div className="hub-qr-box">{qrContent !== null && <QRCodeSVG value={qrContent} size={QR_SIZE} />}</div>
        <p className="hub-field-hint">支付成功后余额自动到账，回到本页刷新即可看到。</p>
      </Modal>
    </Card>
  )
}

/** 自动续费开关（docs/03 §5.16）：到期时按当时售价从余额扣款续上次购买的时长；余额不足会关闭并发邮件通知。 */
function AutoRenewCard({ wallet, onChanged }: { wallet: Wallet; onChanged: () => void }) {
  const toast = useToast()
  const currency = useCurrency()
  const [isBusy, setIsBusy] = useState(false)
  const info = wallet.autoRenew

  const toggle = async () => {
    setIsBusy(true)
    const err = await mutate(() => api.PUT('/api/user/wallet/auto-renew', { params: csrfHeader(), body: { enabled: !info.enabled } }))
    setIsBusy(false)
    if (err !== null) {
      toast.show(err.message, 'error')
      return
    }
    toast.show(info.enabled ? '已关闭自动续费' : '已开启自动续费', 'success')
    onChanged()
  }

  return (
    <Card title="自动续费" extra={<Badge tone={info.enabled ? 'success' : undefined}>{info.enabled ? '已开启' : '未开启'}</Badge>}>
      {info.planName !== '' && (
        <p className="hub-card-text">
          续费对象：{info.planName} · {durationLabel(info.durationDays)}
          {info.price !== null && ` · ${formatMoney(info.price, currency)}`}
        </p>
      )}
      <p className="hub-card-text">到期时按当时的售价从余额扣款并顺延；余额不足会关闭自动续费并发邮件通知，之后充值不会补扣。</p>
      {!info.available && info.reason !== '' && <Alert tone="info">{info.reason}</Alert>}
      <Button variant={info.enabled ? 'outline' : 'primary'} isBusy={isBusy} disabled={!info.enabled && !info.available} onClick={() => void toggle()}>
        {info.enabled ? '关闭自动续费' : '开启自动续费'}
      </Button>
    </Card>
  )
}

function LedgerCard({ version }: { version: number }) {
  const currency = useCurrency()
  const [page, setPage] = useState(1)
  const load = useCallback(
    (signal: AbortSignal) => {
      // version 只用来在余额变动后让请求函数的引用变化，从而重新拉取
      void version
      return api.GET('/api/user/wallet/ledger', { params: { query: { page, pageSize: PAGE_SIZE } }, signal })
    },
    [page, version],
  )
  const ledger = useRemote<PageData<LedgerEntry>>(load)
  const items = ledger.data?.items ?? []
  return (
    <Card title="余额流水">
      {ledger.error !== null && <Alert tone="plain">{ledger.error.message}</Alert>}
      <div className="hub-table-wrap">
        <table className="hub-table">
          <thead>
            <tr>
              <th>时间</th>
              <th>类型</th>
              <th>变动</th>
              <th>余额</th>
              <th>备注</th>
            </tr>
          </thead>
          <tbody>
            {items.map((l) => (
              <tr key={l.id}>
                <td>{formatTime(l.createdAt)}</td>
                <td>{LEDGER_KIND_LABELS[l.kind] ?? l.kind}</td>
                <td className={l.amount < 0 ? 'hub-money-out' : 'hub-money-in'}>{`${l.amount > 0 ? '+' : ''}${formatMoney(l.amount, currency)}`}</td>
                <td>{formatMoney(l.balanceAfter, currency)}</td>
                <td>{l.remark || '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {ledger.data !== null && items.length === 0 && <Empty>还没有流水</Empty>}
      <Pager page={page} pageSize={PAGE_SIZE} total={ledger.data?.total ?? 0} onChange={setPage} />
    </Card>
  )
}

function OrdersCard() {
  const currency = useCurrency()
  const [page, setPage] = useState(1)
  const load = useCallback((signal: AbortSignal) => api.GET('/api/user/orders', { params: { query: { page, pageSize: PAGE_SIZE } }, signal }), [page])
  const orders = useRemote<PageData<Order>>(load)
  const items = orders.data?.items ?? []
  return (
    <Card title="购买记录">
      {orders.error !== null && <Alert tone="plain">{orders.error.message}</Alert>}
      <div className="hub-table-wrap">
        <table className="hub-table">
          <thead>
            <tr>
              <th>时间</th>
              <th>类型</th>
              <th>商品</th>
              <th>实付</th>
              <th>订单号</th>
            </tr>
          </thead>
          <tbody>
            {items.map((o) => (
              <tr key={o.id}>
                <td>{formatTime(o.createdAt)}</td>
                <td>{orderKindLabel(o)}</td>
                <td>{o.kind === 'traffic_pack' ? `${o.itemName}（${formatBytes(o.trafficBytes)}）` : `${o.itemName}（${durationLabel(o.durationDays)}）`}</td>
                <td>
                  {formatMoney(o.amountPaid, currency)}
                  {o.amountDiscount > 0 && <span className="hub-muted">（优惠 {formatMoney(o.amountDiscount, currency)}）</span>}
                </td>
                <td className="hub-mono">{o.orderNo}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {orders.data !== null && items.length === 0 && <Empty>还没有购买记录</Empty>}
      <Pager page={page} pageSize={PAGE_SIZE} total={orders.data?.total ?? 0} onChange={setPage} />
    </Card>
  )
}

/** 我的钱包（docs/06 §5）：余额、充值、自动续费、流水与购买记录。商店关闭时仍可查看余额与记录，但不能兑换与充值。 */
export function WalletPage() {
  const site = useSite()
  const currency = useCurrency()
  const wallet = useRemote<Wallet>(loadWallet)
  const [version, setVersion] = useState(0)
  const data = wallet.data
  const refresh = () => {
    wallet.reload()
    setVersion((v) => v + 1)
  }

  if (wallet.error !== null && data === null) {
    return (
      <>
        <PageHeader title="我的钱包" />
        <Alert tone="plain">{wallet.error.message}</Alert>
      </>
    )
  }
  if (data === null) {
    return <div className="hub-empty">加载中…</div>
  }
  const isShopOpen = site.shopEnabled === true
  return (
    <>
      <PageHeader title="我的钱包" />
      <section className="hub-hero">
        <div>
          <div className="hub-hero-label">账户余额</div>
          <div className="hub-hero-value">{formatMoney(data.balance, currency)}</div>
          <div className="hub-hero-foot">累计获得邀请返利 {formatMoney(data.rebateTotal, currency)} · 余额只能在本站消费，不能提现</div>
        </div>
        {isShopOpen && (
          <Link className="hub-btn hub-btn-ghost" to={ROUTES.SHOP}>
            去商店
          </Link>
        )}
      </section>
      {isShopOpen && (
        <div className="hub-grid-2">
          <div>
            <RedeemCard onRedeemed={refresh} />
            {data.gateways.length > 0 && <OnlineTopupCard gateways={data.gateways} />}
          </div>
          <AutoRenewCard wallet={data} onChanged={wallet.reload} />
        </div>
      )}
      <LedgerCard version={version} />
      <OrdersCard />
    </>
  )
}
