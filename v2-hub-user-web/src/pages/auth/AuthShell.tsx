import { Check } from 'lucide-react'
import type { ReactNode } from 'react'
import authBackground from '../../assets/auth-bg.webp'
import { BrandMark } from '../../components/BrandMark'
import { LocaleSwitch } from '../../components/LocaleSwitch'
import { revealChildren, slideInFromRight } from '../../components/motion'
import { SupportButton } from '../../components/SupportButton'
import { useI18n, type MessageKey } from '../../i18n/i18n-context'
import { useSite } from '../../site/site-context'

const TOP_MARK_SIZE = 28
/** 版权年份：页面加载时取一次即可。 */
const COPYRIGHT_YEAR = new Date(Date.now()).getFullYear()
const CHECK_ICON_SIZE = 16
/** 右侧的三条功能亮点。 */
const FEATURE_KEYS: readonly MessageKey[] = ['auth.feature1', 'auth.feature2', 'auth.feature3']

interface AuthShellProps {
  /** 标题里品牌名前面的字，如“欢迎使用”。 */
  lead: string
  subtitle: string
  children: ReactNode
}

/**
 * 登录、注册、找回密码共用的登录外壳（docs/06 §5 M4 的做法，M10 补充改版）：整页插画背景，
 * 顶栏品牌与语言切换，左侧表单卡片，右侧站点名、标语与功能亮点，底部版权；窄屏只留卡片。
 */
export function AuthShell({ lead, subtitle, children }: AuthShellProps) {
  const site = useSite()
  const { t } = useI18n()
  return (
    <div className="hub-auth" style={{ backgroundImage: `url(${authBackground})` }}>
      <header className="hub-auth-top">
        <span className="hub-auth-brand">
          <BrandMark size={TOP_MARK_SIZE} motion="intro" hasHoverTurn />
          <span>{site.siteName}</span>
        </span>
        <LocaleSwitch />
      </header>
      <main className="hub-auth-main">
        <section className="hub-auth-card" ref={revealChildren}>
          <h1>
            {lead} <strong>{site.siteName}</strong>
          </h1>
          <p className="hub-muted hub-auth-subtitle">{subtitle}</p>
          {children}
        </section>
        <section className="hub-auth-pitch" aria-label={t('shell.slogan')} ref={slideInFromRight}>
          <h2>{site.siteName}</h2>
          <p className="hub-auth-slogan">{t('shell.slogan')}</p>
          <ul className="hub-auth-features">
            {FEATURE_KEYS.map((key) => (
              <li key={key}>
                <Check size={CHECK_ICON_SIZE} aria-hidden="true" />
                <span>{t(key)}</span>
              </li>
            ))}
          </ul>
        </section>
      </main>
      <footer className="hub-auth-footer">
        © {COPYRIGHT_YEAR} {site.siteName}
      </footer>
      <SupportButton />
    </div>
  )
}
