import { useCallback, useState } from 'react'
import { Link } from 'react-router'
import { CODE_INSUFFICIENT_BALANCE, api, csrfHeader, toApiError, type OrderInput, type Quote } from '../../api/client'
import { useToast } from '../../components/toast-context'
import { Alert, Button, Field, Modal } from '../../components/ui'
import { formatBytes, formatTime } from '../../format'
import { useRemote } from '../../hooks/use-remote'
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
function consequence(item: CheckoutItem, quote: Quote): string {
  if (item.input.kind === 'traffic_pack') {
    return '流量包只增加流量额度，到期时间不变；之后续费或换购套餐时，这部分额度不保留。'
  }
  if (quote.isRenewal) {
    return '续费：从当前到期日顺延，已用流量清零，流量额度恢复为套餐的值（签到与流量包加的额度不保留）。'
  }
  if (item.hasOtherPlan) {
    return '换购：新套餐立即生效，当前套餐剩余的时长作废、不折算。'
  }
  return '购买后立即生效。'
}

function CheckoutBody({ item, onClose, onPaid }: { item: CheckoutItem; onClose: () => void; onPaid: () => void }) {
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
        setPayError(err.code === CODE_INSUFFICIENT_BALANCE ? '余额不足，请先充值' : err.message)
        quoteState.reload()
        return
      }
      toast.show(`购买成功：${res.data.data.itemName}`, 'success')
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
      title={`购买：${item.title}`}
      isOpen
      onClose={onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          {isShort ? (
            <Link className="hub-btn hub-btn-primary" to={ROUTES.WALLET}>
              去充值
            </Link>
          ) : (
            <Button variant="primary" isBusy={isPaying} disabled={quote === null || quoteState.isLoading} onClick={() => void handlePay()}>
              确认支付
            </Button>
          )}
        </>
      }
    >
      {payError !== null && <Alert tone="plain">{payError}</Alert>}
      {quoteState.error !== null && <Alert tone="plain">{quoteState.error.message}</Alert>}
      {item.canUseCoupon && (
        <div className="hub-code-row">
          <Field label="优惠码" autoComplete="off" maxLength={MAX_COUPON_LEN} value={couponInput} onChange={(e) => setCouponInput(e.target.value)} />
          <Button variant="outline" disabled={couponInput.trim() === coupon} onClick={() => setCoupon(couponInput.trim())}>
            {couponInput.trim() === '' && coupon !== '' ? '取消优惠码' : '使用'}
          </Button>
        </div>
      )}
      {quote === null && quoteState.error === null && <div className="hub-empty">正在计算价格…</div>}
      {quote !== null && (
        <>
          <dl className="hub-bill">
            <dt>原价</dt>
            <dd>{formatMoney(quote.amountOriginal, currency)}</dd>
            {quote.amountDiscount > 0 && (
              <>
                <dt>优惠（{quote.couponCode}）</dt>
                <dd>−{formatMoney(quote.amountDiscount, currency)}</dd>
              </>
            )}
            <dt className="hub-bill-total">应付</dt>
            <dd className="hub-bill-total">{formatMoney(quote.amountPayable, currency)}</dd>
            <dt>当前余额</dt>
            <dd>{formatMoney(quote.balance, currency)}</dd>
            {isShort && (
              <>
                <dt>还需充值</dt>
                <dd className="hub-bill-short">{formatMoney(quote.shortfall, currency)}</dd>
              </>
            )}
            <dt>购买后的到期时间</dt>
            <dd>{quote.expiresAt === null ? '不过期，或首次使用后开始计时' : formatTime(quote.expiresAt)}</dd>
            <dt>购买后的流量额度</dt>
            <dd>{quote.trafficLimit === UNLIMITED ? '不限' : formatBytes(quote.trafficLimit)}</dd>
          </dl>
          <p className="hub-field-hint">{consequence(item, quote)}</p>
        </>
      )}
    </Modal>
  )
}
