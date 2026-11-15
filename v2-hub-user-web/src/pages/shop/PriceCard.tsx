import { useState, type ReactNode } from 'react'
import type { ShopPlan } from '../../api/client'
import { Segmented } from '../../components/ui'
import { useI18n } from '../../i18n/i18n-context'
import { formatMoney } from '../../money'
import { durationLabel, planFeatureLines } from './labels'

/** 价格卡要用到的套餐字段；商店套餐与落地页公开套餐都满足。 */
export type PriceCardPlan = Pick<ShopPlan, 'name' | 'prices' | 'trafficLimit' | 'trafficReset' | 'limitIp' | 'nodeCount' | 'description'>

interface PriceCardProps {
  plan: PriceCardPlan
  currency: string
  /** 是否用户当前套餐（卡片加 is-current 高亮）。 */
  isCurrent?: boolean
  /** 标题右侧的徽章。 */
  badges?: ReactNode
  /** 卖点列表下方的操作区，参数是当前选中时长的价格 id。 */
  renderFooter: (priceId: number) => ReactNode
}

/**
 * 套餐价格卡（docs/06 §5 商店）：价格、时长、卖点，卡内用分段选择块选时长；商店与落地页共用，徽章与底部操作由调用方给。
 * 套餐没有任何价格时不渲染。
 */
export function PriceCard({ plan, currency, isCurrent = false, badges, renderFooter }: PriceCardProps) {
  const { t, tag } = useI18n()
  const [picked, setPicked] = useState<number | null>(null)
  // 没选过，或选中的选项已不在列表里时，落到第一个
  const price = plan.prices.find((p) => p.id === picked) ?? plan.prices[0]
  if (price === undefined) {
    return null
  }
  return (
    <section className={`hub-price-card${isCurrent ? ' is-current' : ''}`}>
      <header className="hub-price-head">
        <h4>{plan.name}</h4>
        {badges}
      </header>
      <div className="hub-price-amount">{formatMoney(price.price, currency, tag)}</div>
      <div className="hub-price-period">/ {durationLabel(t, price.durationDays)}</div>
      {plan.prices.length > 1 && (
        <Segmented
          aria="radio"
          aria-label={t('shop.durationOf', { name: plan.name })}
          options={plan.prices.map((p) => ({ value: p.id, label: durationLabel(t, p.durationDays) }))}
          value={price.id}
          onChange={setPicked}
        />
      )}
      <ul className="hub-price-features">
        {planFeatureLines(t, plan).map((line) => (
          <li key={line}>{line}</li>
        ))}
      </ul>
      {renderFooter(price.id)}
    </section>
  )
}
