import { useCallback, useState } from 'react'
import { Link } from 'react-router'
import { CODE_INSUFFICIENT_BALANCE, api, csrfHeader, toApiError, type OrderInput, type Quote } from '../../api/client'
import { useToast } from '../../components/toast-context'
import { Alert, Button, Field, Modal } from '../../components/ui'
import { formatBytes, formatTime } from '../../format'
import { useRemote } from '../../hooks/use-remote'
import { useI18n, type MessageKey } from '../../i18n/i18n-context'
import { ROUTES } from '../../menu'
import { formatMoney } from '../../money'
import { useCurrency } from '../../site/site-context'

const MAX_COUPON_LEN = 32
const UNLIMITED = 0

/** 要结算的一件商品：下单参数（不含优惠码）加上展示用的名称与说明。 */
export interface CheckoutItem {
  title: string
  input: Omit<OrderInput, 'couponCode'>
  /** 是否允许填优惠码（试用套餐不允许）。 */
  canUseCoupon: boolean
  /** 用户当前是否持有别的套餐：买套餐时据此提示“换购会作废原剩余时长”。 */
  hasOtherPlan: boolean
}

interface CheckoutModalProps {
  /** null = 关闭。 */
  item: CheckoutItem | null
  onClose: () => void
  /** 下单成功后通知上层刷新商店数据。 */
  onPaid: () => void
}

/** 账单式结算框（docs/06 §5 M5 的做法）：关闭时不渲染，优惠码与询价状态随之重置。 */
export function CheckoutModal({ item, onClose, onPaid }: CheckoutModalProps) {
  if (item === null) {
    return null
  }
  return <CheckoutBody item={item} onClose={onClose} onPaid={onPaid} />
}

/** 这次购买的后果说明：续费、换购、首次购买、流量包各不相同（docs/03 §5.13）。 */
function consequence(item: CheckoutItem, quote: Quote): MessageKey {
  if (item.input.kind === 'traffic_pack') {
    return 'checkout.packEffect'
  }
  if (quote.isRenewal) {
    return 'checkout.renewEffect'
  }
  if (item.hasOtherPlan) {
    return 'checkout.switchEffect'
  }
  return 'checkout.newEffect'
}

function CheckoutBody({ item, onClose, onPaid }: { item: CheckoutItem; onClose: () => void; onPaid: () => void }) {
  const { t, tag } = useI18n()
  const toast = useToast()
  const currency = useCurrency()
  const [couponInput, setCouponInput] = useState('')
  const [coupon, setCoupon] = useState('')
  const [isPaying, setIsPaying] = useState(false)
  const [payError, setPayError] = useState<string | null>(null)
  const { input } = item

  // 询价不落库、不占优惠码次数；商品或已应用的优惠码一变就重新询价，页面上的金额全部取它的结果
  const loadQuote = useCallback(
    (signal: AbortSignal) => api.POST('/api/user/shop/quote', { params: csrfHeader(), body: { ...input, couponCode: coupon === '' ? undefined : coupon }, signal }),
    [input, coupon],
  )
  const quoteState = useRemote<Quote>(loadQuote)
  // 询价失败（如优惠码无效）时不沿用上一次的金额，避免按错的价格确认
  const quote = quoteState.error === null ? quoteState.data : null

  const handlePay = async () => {
    setIsPaying(true)
    setPayError(null)
    try {
      const res = await api.POST('/api/user/orders', { params: csrfHeader(), body: { ...input, couponCode: coupon === '' ? undefined : coupon } })
      if (res.error !== undefined || res.data?.data === undefined) {
        const err = toApiError(res.error)
        setPayError(err.code === CODE_INSUFFICIENT_BALANCE ? t('checkout.insufficient') : err.message)
        quoteState.reload()
        return
      }
      toast.show(t('checkout.paid', { name: res.data.data.itemName }), 'success')
      onPaid()
      onClose()
    } catch (err) {
      reportError(err)
      setPayError(toApiError(err).message)
    } finally {
      setIsPaying(false)
    }
  }

  const isShort = quote !== null && quote.shortfall > 0
  return (
    <Modal
      title={t('checkout.title', { name: item.title })}
      isOpen
      onClose={onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          {isShort ? (
            <Link className="hub-btn hub-btn-primary" to={ROUTES.WALLET}>
              {t('checkout.goTopup')}
            </Link>
          ) : (
            <Button variant="primary" isBusy={isPaying} disabled={quote === null || quoteState.isLoading} onClick={() => void handlePay()}>
              {t('checkout.pay')}
            </Button>
          )}
        </>
      }
    >
      {payError !== null && <Alert tone="plain">{payError}</Alert>}
      {quoteState.error !== null && <Alert tone="plain">{quoteState.error.message}</Alert>}
      {item.canUseCoupon && (
        <div className="hub-code-row">
          <Field label={t('checkout.coupon')} autoComplete="off" maxLength={MAX_COUPON_LEN} value={couponInput} onChange={(e) => setCouponInput(e.target.value)} />
          <Button variant="outline" disabled={couponInput.trim() === coupon} onClick={() => setCoupon(couponInput.trim())}>
            {t(couponInput.trim() === '' && coupon !== '' ? 'checkout.removeCoupon' : 'checkout.applyCoupon')}
          </Button>
        </div>
      )}
      {quote === null && quoteState.error === null && <div className="hub-empty">{t('checkout.quoting')}</div>}
      {quote !== null && (
        <>
          <dl className="hub-bill">
            <dt>{t('checkout.original')}</dt>
            <dd>{formatMoney(quote.amountOriginal, currency, tag)}</dd>
            {quote.amountDiscount > 0 && (
              <>
                <dt>{t('checkout.discount', { code: quote.couponCode })}</dt>
                <dd>−{formatMoney(quote.amountDiscount, currency, tag)}</dd>
              </>
            )}
            <dt className="hub-bill-total">{t('checkout.due')}</dt>
            <dd className="hub-bill-total">{formatMoney(quote.amountPayable, currency, tag)}</dd>
            <dt>{t('checkout.balance')}</dt>
            <dd>{formatMoney(quote.balance, currency, tag)}</dd>
            {isShort && (
              <>
                <dt>{t('checkout.shortfall')}</dt>
                <dd className="hub-bill-short">{formatMoney(quote.shortfall, currency, tag)}</dd>
              </>
            )}
            <dt>{t('checkout.expiresAfter')}</dt>
            <dd>{quote.expiresAt === null ? t('checkout.noExpiry') : formatTime(quote.expiresAt, tag)}</dd>
            <dt>{t('checkout.trafficAfter')}</dt>
            <dd>{quote.trafficLimit === UNLIMITED ? t('common.unlimited') : formatBytes(quote.trafficLimit)}</dd>
          </dl>
          <p className="hub-field-hint">{t(consequence(item, quote))}</p>
        </>
      )}
    </Modal>
  )
}
