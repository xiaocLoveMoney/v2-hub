import { useState } from 'react'
import { Link } from 'react-router'
import { api, type Shop, type ShopPlan, type ShopTrafficPack } from '../../api/client'
import { Alert, Badge, Button, Card, Empty, Loading, PageHeader } from '../../components/ui'
import { formatBytes } from '../../format'
import { useRemote } from '../../hooks/use-remote'
import { useI18n } from '../../i18n/i18n-context'
import { ROUTES } from '../../menu'
import { formatMoney } from '../../money'
import { useCurrency } from '../../site/site-context'
import { CheckoutModal, type CheckoutItem } from './CheckoutModal'
import { PriceCard } from './PriceCard'

const loadShop = (signal: AbortSignal) => api.GET('/api/user/shop', { signal })

interface PlanCardProps {
  plan: ShopPlan
  currency: string
  onBuy: (plan: ShopPlan, priceId: number) => void
}

/** 商店的套餐卡：当前套餐与试用带徽章，底部是购买 / 续费 / 试用按钮及不能购买的原因。 */
function PlanCard({ plan, currency, onBuy }: PlanCardProps) {
  const { t } = useI18n()
  const action = t(plan.isTrial ? 'shop.freeTrial' : plan.isCurrent ? 'shop.renew' : 'shop.buy')
  return (
    <PriceCard
      plan={plan}
      currency={currency}
      isCurrent={plan.isCurrent}
      badges={
        <>
          {plan.isCurrent && <Badge tone="primary">{t('shop.currentPlan')}</Badge>}
          {plan.isTrial && <Badge tone="warning">{t('shop.trial')}</Badge>}
        </>
      }
      renderFooter={(priceId) => (
        <>
          <Button variant="primary" isBlock disabled={!plan.canBuy} onClick={() => onBuy(plan, priceId)}>
            {action}
          </Button>
          {!plan.canBuy && plan.reason !== '' && <p className="hub-field-hint">{plan.reason}</p>}
        </>
      )}
    />
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
    return <Loading />
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
