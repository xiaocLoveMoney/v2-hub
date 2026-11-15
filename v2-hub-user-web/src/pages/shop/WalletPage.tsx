import { QRCodeSVG } from 'qrcode.react'
import { useCallback, useState, type FormEvent } from 'react'
import { Link } from 'react-router'
import { api, csrfHeader, type LedgerEntry, type Order, type Wallet } from '../../api/client'
import { HTTPS_PREFIX } from '../../app-config'
import { useToast } from '../../components/toast-context'
import { Alert, Badge, Button, Card, DataTable, Field, Loading, Modal, PageHeader, Pager, Segmented } from '../../components/ui'
import { formatBytes, formatTime } from '../../format'
import { mutate, request, usePagedRemote, useRemote } from '../../hooks/use-remote'
import { useI18n } from '../../i18n/i18n-context'
import { ROUTES } from '../../menu'
import { formatMoney, parseAmountToCents } from '../../money'
import { useCurrency, useSite } from '../../site/site-context'
import { LEDGER_KIND_KEYS, durationLabel, orderKindLabel } from './labels'

const PAGE_SIZE = 10
const MAX_CODE_INPUT_LEN = 24
const MIN_CODE_INPUT_LEN = 16
const QR_SIZE = 200
/** 在线充值的最低金额（分），与 hub-api.yaml 的 PaymentCreateInput 一致。 */
const MIN_TOPUP_CENTS = 100

const loadWallet = (signal: AbortSignal) => api.GET('/api/user/wallet', { signal })
const loadOrders = (page: number, signal: AbortSignal) => api.GET('/api/user/orders', { params: { query: { page, pageSize: PAGE_SIZE } }, signal })

/** 兑换充值码：成功后余额立即到账（docs/03 §5.15）。 */
function RedeemCard({ onRedeemed }: { onRedeemed: () => void }) {
  const { t, tag } = useI18n()
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
      const result = await request(() => api.POST('/api/user/wallet/redeem', { params: csrfHeader(), body: { code: code.trim() } }))
      if (result.error !== null) {
        setError(result.error.message)
        return
      }
      setCode('')
      toast.show(t('wallet.redeemed', { amount: formatMoney(result.data.amount, currency, tag) }), 'success')
      onRedeemed()
    } finally {
      setIsBusy(false)
    }
  }

  return (
    <Card title={t('wallet.redeemTitle')}>
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <form onSubmit={(e) => void handleSubmit(e)} noValidate>
        <Field label={t('wallet.code')} autoComplete="off" maxLength={MAX_CODE_INPUT_LEN} hint={t('wallet.codeHint')} value={code} onChange={(e) => setCode(e.target.value)} />
        <Button type="submit" variant="primary" isBusy={isBusy} disabled={code.trim().length < MIN_CODE_INPUT_LEN}>
          {t('wallet.redeem')}
        </Button>
      </form>
    </Card>
  )
}

/** 在线充值：站点配了支付网关才出现；网关给跳转地址就跳转，给二维码内容就展示二维码（docs/03 §5.15）。 */
function OnlineTopupCard({ gateways }: { gateways: Wallet['gateways'] }) {
  const { t } = useI18n()
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
      const result = await request(() => api.POST('/api/user/wallet/payments', { params: csrfHeader(), body: { gateway, amount: cents } }))
      if (result.error !== null) {
        setError(result.error.message)
        return
      }
      const { payUrl, qrContent: qr } = result.data
      // 只跟随 https 的支付地址，其余一律不跳
      if (payUrl.startsWith(HTTPS_PREFIX)) {
        window.location.assign(payUrl)
        return
      }
      if (qr !== '') {
        setQrContent(qr)
        return
      }
      setError(t('wallet.noPayEntry'))
    } finally {
      setIsBusy(false)
    }
  }

  return (
    <Card title={t('wallet.onlineTitle')}>
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <form onSubmit={(e) => void handleSubmit(e)} noValidate>
        <Segmented
          aria="radio"
          className="hub-row-gap"
          aria-label={t('wallet.payMethod')}
          options={gateways.map((g) => ({ value: g.code, label: g.name }))}
          value={gateway}
          onChange={setGateway}
        />
        <Field label={t('wallet.amount')} inputMode="decimal" autoComplete="off" hint={t('wallet.amountHint')} value={amount} onChange={(e) => setAmount(e.target.value)} />
        <Button type="submit" variant="primary" isBusy={isBusy} disabled={cents === null || cents < MIN_TOPUP_CENTS || gateway === ''}>
          {t('wallet.pay')}
        </Button>
      </form>
      <Modal title={t('wallet.scanTitle')} isOpen={qrContent !== null} onClose={() => setQrContent(null)}>
        <div className="hub-qr-box">{qrContent !== null && <QRCodeSVG value={qrContent} size={QR_SIZE} />}</div>
        <p className="hub-field-hint">{t('wallet.scanHint')}</p>
      </Modal>
    </Card>
  )
}

/** 自动续费开关（docs/03 §5.16）：到期时按当时售价从余额扣款续上次购买的时长；余额不足会关闭并发邮件通知。 */
function AutoRenewCard({ wallet, onChanged }: { wallet: Wallet; onChanged: () => void }) {
  const { t, tag } = useI18n()
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
    toast.show(t(info.enabled ? 'wallet.autoRenewOff' : 'wallet.autoRenewOnDone'), 'success')
    onChanged()
  }

  return (
    <Card title={t('wallet.autoRenewTitle')} extra={<Badge tone={info.enabled ? 'success' : undefined}>{t(info.enabled ? 'wallet.on' : 'wallet.off')}</Badge>}>
      {info.planName !== '' && (
        <p className="hub-card-text">
          {t('wallet.renewTarget', { plan: info.planName, duration: durationLabel(t, info.durationDays) })}
          {info.price !== null && ` · ${formatMoney(info.price, currency, tag)}`}
        </p>
      )}
      <p className="hub-card-text">{t('wallet.renewRule')}</p>
      {!info.available && info.reason !== '' && <Alert tone="info">{info.reason}</Alert>}
      <Button variant={info.enabled ? 'outline' : 'primary'} isBusy={isBusy} disabled={!info.enabled && !info.available} onClick={() => void toggle()}>
        {t(info.enabled ? 'wallet.turnOff' : 'wallet.turnOn')}
      </Button>
    </Card>
  )
}

function LedgerCard({ version }: { version: number }) {
  const { t, tag } = useI18n()
  const currency = useCurrency()
  const loadPage = useCallback(
    (page: number, signal: AbortSignal) => {
      // version 只用来在余额变动后让请求函数的引用变化，从而重新拉取
      void version
      return api.GET('/api/user/wallet/ledger', { params: { query: { page, pageSize: PAGE_SIZE } }, signal })
    },
    [version],
  )
  const ledger = usePagedRemote<LedgerEntry>(loadPage)
  const items = ledger.data?.items ?? []
  return (
    <Card title={t('wallet.ledgerTitle')}>
      {ledger.error !== null && <Alert tone="plain">{ledger.error.message}</Alert>}
      <DataTable headers={[t('wallet.colTime'), t('wallet.colKind'), t('wallet.colChange'), t('wallet.colBalance'), t('wallet.colRemark')]} isEmpty={ledger.data !== null && items.length === 0} emptyText={t('wallet.noLedger')}>
        {items.map((l) => (
          <tr key={l.id}>
            <td>{formatTime(l.createdAt, tag)}</td>
            <td>{l.kind in LEDGER_KIND_KEYS ? t(LEDGER_KIND_KEYS[l.kind]) : l.kind}</td>
            <td className={l.amount < 0 ? 'hub-money-out' : 'hub-money-in'}>{`${l.amount > 0 ? '+' : ''}${formatMoney(l.amount, currency, tag)}`}</td>
            <td>{formatMoney(l.balanceAfter, currency, tag)}</td>
            <td>{l.remark || t('common.dash')}</td>
          </tr>
        ))}
      </DataTable>
      <Pager page={ledger.page} pageSize={PAGE_SIZE} total={ledger.data?.total ?? 0} onChange={ledger.setPage} />
    </Card>
  )
}

function OrdersCard() {
  const { t, tag } = useI18n()
  const currency = useCurrency()
  const orders = usePagedRemote<Order>(loadOrders)
  const items = orders.data?.items ?? []
  return (
    <Card title={t('wallet.ordersTitle')}>
      {orders.error !== null && <Alert tone="plain">{orders.error.message}</Alert>}
      <DataTable headers={[t('wallet.colTime'), t('wallet.colKind'), t('wallet.colItem'), t('wallet.colPaid'), t('wallet.colOrderNo')]} isEmpty={orders.data !== null && items.length === 0} emptyText={t('wallet.noOrders')}>
        {items.map((o) => (
          <tr key={o.id}>
            <td>{formatTime(o.createdAt, tag)}</td>
            <td>{orderKindLabel(t, o)}</td>
            <td>{t('wallet.itemWithDetail', { name: o.itemName, detail: o.kind === 'traffic_pack' ? formatBytes(o.trafficBytes) : durationLabel(t, o.durationDays) })}</td>
            <td>
              {formatMoney(o.amountPaid, currency, tag)}
              {o.amountDiscount > 0 && <span className="hub-muted">{t('wallet.discount', { amount: formatMoney(o.amountDiscount, currency, tag) })}</span>}
            </td>
            <td className="hub-mono">{o.orderNo}</td>
          </tr>
        ))}
      </DataTable>
      <Pager page={orders.page} pageSize={PAGE_SIZE} total={orders.data?.total ?? 0} onChange={orders.setPage} />
    </Card>
  )
}

/** 我的钱包（docs/06 §5）：余额、充值、自动续费、流水与购买记录。商店关闭时仍可查看余额与记录，但不能兑换与充值。 */
export function WalletPage() {
  const { t, tag } = useI18n()
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
        <PageHeader title={t('wallet.title')} />
        <Alert tone="plain">{wallet.error.message}</Alert>
      </>
    )
  }
  if (data === null) {
    return <Loading />
  }
  const isShopOpen = site.shopEnabled === true
  return (
    <>
      <PageHeader title={t('wallet.title')} />
      <section className="hub-hero">
        <div>
          <div className="hub-hero-label">{t('wallet.balanceLabel')}</div>
          <div className="hub-hero-value">{formatMoney(data.balance, currency, tag)}</div>
          <div className="hub-hero-foot">{t('wallet.balanceFoot', { amount: formatMoney(data.rebateTotal, currency, tag) })}</div>
        </div>
        {isShopOpen && (
          <Link className="hub-btn hub-btn-ghost" to={ROUTES.SHOP}>
            {t('wallet.goShop')}
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
