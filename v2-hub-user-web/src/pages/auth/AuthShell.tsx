import type { ReactNode } from 'react'
import { BrandMark } from '../../components/BrandMark'
import { LocaleSwitch } from '../../components/LocaleSwitch'
import { revealChildren, slideInFromRight } from '../../components/motion'
import { SupportButton } from '../../components/SupportButton'
import { useI18n } from '../../i18n/i18n-context'
import { useSite } from '../../site/site-context'

/** 登录页右侧的大图标尺寸（像素）。 */
const ART_MARK_SIZE = 72

interface AuthShellProps {
  /** 标题里品牌名前面的字，如“欢迎使用”。 */
  lead: string
  subtitle: string
  children: ReactNode
}

/** 登录、注册、找回密码共用的分屏外壳（docs/06 §5）：左侧表单，右侧全高背景与标语。 */
export function AuthShell({ lead, subtitle, children }: AuthShellProps) {
  const site = useSite()
  const { t } = useI18n()
  return (
    <div className="hub-login">
      <section className="hub-login-form" ref={revealChildren}>
        <h1>
          {lead} <strong>{site.siteName}</strong>
        </h1>
        <p className="hub-muted hub-login-subtitle">{subtitle}</p>
        {children}
        <div className="hub-login-footer">
          <LocaleSwitch />
        </div>
      </section>
      <section className="hub-login-art" aria-hidden="true" ref={slideInFromRight}>
        <div>
          <BrandMark size={ART_MARK_SIZE} motion="intro" className="hub-login-art-mark" />
          <h2>{site.siteName}</h2>
          <p>{t('shell.slogan')}</p>
        </div>
      </section>
      <SupportButton />
    </div>
  )
}
