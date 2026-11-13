import { useState } from 'react'
import { Link } from 'react-router'
import { api, type Shop, type ShopPlan, type ShopTrafficPack } from '../../api/client'
import { Alert, Badge, Button, Card, Empty, PageHeader } from '../../components/ui'
import { formatBytes } from '../../format'
import { useRemote } from '../../hooks/use-remote'
import { ROUTES } from '../../menu'
import { formatMoney } from '../../money'
import { useCurrency } from '../../site/site-context'
import { CheckoutModal, type CheckoutItem } from './CheckoutModal'
import { durationLabel, trafficLabel } from './labels'

const UNLIMITED = 0

const loadShop = (signal: AbortSignal) => api.GET('/api/user/shop', { signal })

interface PlanCardProps {
  plan: ShopPlan
  currency: string
  onBuy: (plan: ShopPlan, priceId: number) => void
}

/** 一张价格卡：卖点、流量、设备数、节点数，卡内用分段选择块选时长（docs/06 §5 商店）。 */
function PlanCard({ plan, currency, onBuy }: PlanCardProps) {
  const [picked, setPicked] = useState<number | null>(null)
  // 没选过，或选中的选项已不在列表里时，落到第一个
  const price = plan.prices.find((p) => p.id === picked) ?? plan.prices[0]
  if (price === undefined) {
    return null
  }
  const features = plan.description.split('\n').filter((line) => line.trim() !== '')
  const action = plan.isTrial ? '免费试用' : plan.isCurrent ? '续费' : '购买'
  return (
    <section className={`hub-price-card${plan.isCurrent ? ' is-current' : ''}`}>
      <header className="hub-price-head">
        <h4>{plan.name}</h4>
        {plan.isCurrent && <Badge tone="primary">当前套餐</Badge>}
        {plan.isTrial && <Badge tone="warning">试用</Badge>}
      </header>
      <div className="hub-price-amount">{formatMoney(price.price, currency)}</div>
      <div className="hub-price-period">/ {durationLabel(price.durationDays)}</div>
      {plan.prices.length > 1 && (
        <div className="hub-seg" role="radiogroup" aria-label={`${plan.name} 的时长`}>
          {plan.prices.map((p) => (
            <button key={p.id} type="button" role="radio" aria-checked={p.id === price.id} className={`hub-seg-item${p.id === price.id ? ' is-active' : ''}`} onClick={() => setPicked(p.id)}>
              {durationLabel(p.durationDays)}
            </button>
          ))}
        </div>
      )}
      <ul className="hub-price-features">
        <li>{trafficLabel(plan.trafficLimit, plan.trafficReset)}</li>
        <li>{plan.limitIp === UNLIMITED ? '不限同时在线设备' : `最多 ${plan.limitIp} 台设备同时在线`}</li>
        <li>{plan.nodeCount} 个节点</li>
        {features.map((line) => (
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
  const currency = useCurrency()
  const shop = useRemote<Shop>(loadShop)
  const [item, setItem] = useState<CheckoutItem | null>(null)
  const data = shop.data

  if (shop.error !== null) {
    return (
      <>
        <PageHeader title="商店" />
        <Alert tone="plain">{shop.error.message}</Alert>
      </>
    )
  }
  if (data === null) {
    return <div className="hub-empty">加载中…</div>
  }

  const hasCurrent = data.plans.some((p) => p.isCurrent)
  const buyPlan = (plan: ShopPlan, priceId: number) =>
    setItem({ title: plan.name, input: { kind: 'plan', planPriceId: priceId }, canUseCoupon: !plan.isTrial, hasOtherPlan: hasCurrent && !plan.isCurrent })
  const buyPack = (pack: ShopTrafficPack) =>
    setItem({ title: pack.name, input: { kind: 'traffic_pack', trafficPackId: pack.id }, canUseCoupon: true, hasOtherPlan: false })

  return (
    <>
      <PageHeader
        title="商店"
        extra={
          <span className="hub-actions">
            <span>
              余额 <strong>{formatMoney(data.balance, currency)}</strong>
            </span>
            <Link className="hub-btn hub-btn-outline" to={ROUTES.WALLET}>
              充值
            </Link>
          </span>
        }
      />
      {data.plans.length === 0 && (
        <Card>
          <Empty>暂时没有在售的套餐</Empty>
        </Card>
      )}
      <div className="hub-price-grid">
        {data.plans.map((plan) => (
          <PlanCard key={plan.id} plan={plan} currency={currency} onBuy={buyPlan} />
        ))}
      </div>
      {data.trafficPacks.length > 0 && (
        <>
          <h2 className="hub-section-title">流量包</h2>
          {!data.canBuyTrafficPack && data.trafficPackReason !== '' && <Alert tone="info">{data.trafficPackReason}</Alert>}
          <div className="hub-price-grid">
            {data.trafficPacks.map((pack) => (
              <section key={pack.id} className="hub-price-card">
                <header className="hub-price-head">
                  <h4>{pack.name}</h4>
                </header>
                <div className="hub-price-amount">{formatMoney(pack.price, currency)}</div>
                <div className="hub-price-period">+ {formatBytes(pack.trafficBytes)}</div>
                <ul className="hub-price-features">
                  <li>只加流量额度，到期时间不变</li>
                  <li>续费或换购套餐后不保留</li>
                </ul>
                <Button variant="primary" isBlock disabled={!data.canBuyTrafficPack} onClick={() => buyPack(pack)}>
                  购买
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
