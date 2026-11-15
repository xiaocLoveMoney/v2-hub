import { useCallback } from 'react'
import { Link } from 'react-router'
import { api, type PublicPlan } from '../api/client'
import { BrandMark } from '../components/BrandMark'
import { LocaleSwitch } from '../components/LocaleSwitch'
import { Markdown } from '../components/Markdown'
import { revealChildren } from '../components/motion'
import { SupportButton } from '../components/SupportButton'
import { useRemote } from '../hooks/use-remote'
import { useI18n } from '../i18n/i18n-context'
import { ROUTES } from '../menu'
import { REGISTER_MODE_CLOSED, useCurrency, useSite } from '../site/site-context'
import { PriceCard } from './shop/PriceCard'

/** 顶栏品牌位的图标尺寸（像素）。 */
const BRAND_MARK_SIZE = 26

interface PublicPlanCardProps {
  plan: PublicPlan
  currency: string
  /** 购买按钮去哪：开放注册时去注册，否则去登录。 */
  buyTo: string
}

/** 落地页的套餐卡：与商店价格卡同一外观，但只有公开信息，购买引导去注册或登录。 */
function PublicPlanCard({ plan, currency, buyTo }: PublicPlanCardProps) {
  const { t } = useI18n()
  return (
    <PriceCard
      plan={plan}
      currency={currency}
      renderFooter={() => (
        <Link className="hub-btn hub-btn-primary hub-btn-block" to={buyTo}>
          {t('landing.buy')}
        </Link>
      )}
    />
  )
}

/**
 * 落地页（docs/03 §5.20，Ruling-44）：未登录访问根地址且站点开启时显示。标题、Markdown 介绍、在售套餐、登录与注册入口；
 * 介绍与套餐名、卖点是运营内容，不翻译。
 */
export function LandingPage() {
  const site = useSite()
  const currency = useCurrency()
  const { t } = useI18n()
  const loadPlans = useCallback((signal: AbortSignal) => api.GET('/api/user/site/plans', { signal }), [])
  const plans = useRemote<PublicPlan[]>(loadPlans)
  const canRegister = site.registerMode !== REGISTER_MODE_CLOSED
  const headline = site.landing?.headline === undefined || site.landing.headline === '' ? site.siteName : site.landing.headline
  const intro = site.landing?.intro ?? ''
  const items = plans.data ?? []

  return (
    <div className="hub-landing" ref={revealChildren}>
      <header className="hub-landing-nav">
        <span className="hub-brand">
          <BrandMark size={BRAND_MARK_SIZE} motion="intro" hasHoverTurn />
          <span>{site.siteName}</span>
        </span>
        <span className="hub-actions">
          <Link className="hub-btn hub-btn-outline" to={ROUTES.LOGIN}>
            {t('auth.login')}
          </Link>
          {canRegister && (
            <Link className="hub-btn hub-btn-primary" to={ROUTES.REGISTER}>
              {t('auth.register')}
            </Link>
          )}
        </span>
      </header>
      <section className="hub-landing-hero">
        <h1>{headline}</h1>
        {intro !== '' && <Markdown source={intro} />}
        <span className="hub-actions">
          {canRegister && (
            <Link className="hub-btn hub-btn-lg hub-btn-light" to={ROUTES.REGISTER}>
              {t('auth.registerAccount')}
            </Link>
          )}
          <Link className="hub-btn hub-btn-lg hub-btn-ghost" to={ROUTES.LOGIN}>
            {t('auth.login')}
          </Link>
        </span>
      </section>
      {items.length > 0 && (
        <section className="hub-landing-plans">
          <h2 className="hub-section-title">{t('landing.plansTitle')}</h2>
          <div className="hub-price-grid">
            {items.map((plan) => (
              <PublicPlanCard key={plan.id} plan={plan} currency={currency} buyTo={canRegister ? ROUTES.REGISTER : ROUTES.LOGIN} />
            ))}
          </div>
        </section>
      )}
      <footer className="hub-landing-footer">
        <span>{site.siteName}</span>
        <LocaleSwitch />
      </footer>
      <SupportButton />
    </div>
  )
}
