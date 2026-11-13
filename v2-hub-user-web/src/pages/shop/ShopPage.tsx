import { useState } from 'react'
import { Link } from 'react-router'
import { api, type Shop, type ShopPlan, type ShopTrafficPack } from '../../api/client'
import { Alert, Badge, Button, Card, Empty, PageHeader } from '../../components/ui'
import { formatBytes } from '../../format'
import { useRemote } from '../../hooks/use-remote'
import { useI18n } from '../../i18n/i18n-context'
import { ROUTES } from '../../menu'
import { formatMoney } from '../../money'
import { useCurrency } from '../../site/site-context'
import { CheckoutModal, type CheckoutItem } from './CheckoutModal'
import { durationLabel, planFeatureLines } from './labels'

const loadShop = (signal: AbortSignal) => api.GET('/api/user/shop', { signal })

interface PlanCardProps {
  plan: ShopPlan
  currency: string
  onBuy: (plan: ShopPlan, priceId: number) => void
}

/** 一张价格卡：卖点、流量、设备数、节点数，卡内用分段选择块选时长（docs/06 §5 商店）。 */
function PlanCard({ plan, currency, onBuy }: PlanCardProps) {
  const { t, tag } = useI18n()
  const [picked, setPicked] = useState<number | null>(null)
  // 没选过，或选中的选项已不在列表里时，落到第一个
  const price = plan.prices.find((p) => p.id === picked) ?? plan.prices[0]
  if (price === undefined) {
    return null
  }
  const action = t(plan.isTrial ? 'shop.freeTrial' : plan.isCurrent ? 'shop.renew' : 'shop.buy')
  return (
    <section className={`hub-price-card${plan.isCurrent ? ' is-current' : ''}`}>
      <header className="hub-price-head">
        <h4>{plan.name}</h4>
        {plan.isCurrent && <Badge tone="primary">{t('shop.currentPlan')}</Badge>}
        {plan.isTrial && <Badge tone="warning">{t('shop.trial')}</Badge>}
      </header>
      <div className="hub-price-amount">{formatMoney(price.price, currency, tag)}</div>
      <div className="hub-price-period">/ {durationLabel(t, price.durationDays)}</div>
      {plan.prices.length > 1 && (
        <div className="hub-seg" role="radiogroup" aria-label={t('shop.durationOf', { name: plan.name })}>
          {plan.prices.map((p) => (
            <button key={p.id} type="button" role="radio" aria-checked={p.id === price.id} className={`hub-seg-item${p.id === price.id ? ' is-active' : ''}`} onClick={() => setPicked(p.id)}>
              {durationLabel(t, p.durationDays)}
            </button>
          ))}
        </div>
      )}
      <ul className="hub-price-features">
        {planFeatureLines(t, plan).map((line) => (
          <li key={line}>{line}</li>
        ))}
      </ul>
      <Button variant="primary" isBlock disabled={!plan.canBuy} onClick={() => onBuy(plan, price.id)}>
        {action}
      </Button>
      {!plan.canBuy && plan.reason !== '' && <p className="hub-field-hint">{plan.reason}</p>}
    </section>
  )
}

/** 商店（docs/03 §5.12、§5.13）：上架的套餐与流量包；购买一律从余额扣款，余额不足时引导去钱包充值。 */
export function ShopPage() {
  const { t, tag } = useI18n()
  const currency = useCurrency()
  const shop = useRemote<Shop>(loadShop)
  const [item, setItem] = useState<CheckoutItem | null>(null)
  const data = shop.data

  if (shop.error !== null) {
    return (
      <>
        <PageHeader title={t('menu.shop')} />
        <Alert tone="plain">{shop.error.message}</Alert>
      </>
    )
  }
  if (data === null) {
    return <div className="hub-empty">{t('common.loading')}</div>
  }

  const hasCurrent = data.plans.some((p) => p.isCurrent)
  const buyPlan = (plan: ShopPlan, priceId: number) =>
    setItem({ title: plan.name, input: { kind: 'plan', planPriceId: priceId }, canUseCoupon: !plan.isTrial, hasOtherPlan: hasCurrent && !plan.isCurrent })
  const buyPack = (pack: ShopTrafficPack) =>
    setItem({ title: pack.name, input: { kind: 'traffic_pack', trafficPackId: pack.id }, canUseCoupon: true, hasOtherPlan: false })

  return (
    <>
      <PageHeader
        title={t('menu.shop')}
        extra={
          <span className="hub-actions">
            <span>
              {t('shop.balance')} <strong>{formatMoney(data.balance, currency, tag)}</strong>
            </span>
            <Link className="hub-btn hub-btn-outline" to={ROUTES.WALLET}>
              {t('shop.topup')}
            </Link>
          </span>
        }
      />
      {data.plans.length === 0 && (
        <Card>
          <Empty>{t('shop.noPlans')}</Empty>
        </Card>
      )}
      <div className="hub-price-grid">
        {data.plans.map((plan) => (
          <PlanCard key={plan.id} plan={plan} currency={currency} onBuy={buyPlan} />
        ))}
      </div>
      {data.trafficPacks.length > 0 && (
        <>
          <h2 className="hub-section-title">{t('shop.trafficPacks')}</h2>
          {!data.canBuyTrafficPack && data.trafficPackReason !== '' && <Alert tone="info">{data.trafficPackReason}</Alert>}
          <div className="hub-price-grid">
            {data.trafficPacks.map((pack) => (
              <section key={pack.id} className="hub-price-card">
                <header className="hub-price-head">
                  <h4>{pack.name}</h4>
                </header>
                <div className="hub-price-amount">{formatMoney(pack.price, currency, tag)}</div>
                <div className="hub-price-period">+ {formatBytes(pack.trafficBytes)}</div>
                <ul className="hub-price-features">
                  <li>{t('shop.packNote1')}</li>
                  <li>{t('shop.packNote2')}</li>
                </ul>
                <Button variant="primary" isBlock disabled={!data.canBuyTrafficPack} onClick={() => buyPack(pack)}>
                  {t('shop.buy')}
                </Button>
              </section>
            ))}
          </div>
        </>
      )}
      <CheckoutModal item={item} onClose={() => setItem(null)} onPaid={shop.reload} />
    </>
  )
}
