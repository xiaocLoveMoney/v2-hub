import type { ReactNode } from 'react'
import { LocaleSwitch } from '../../components/LocaleSwitch'
import { SupportButton } from '../../components/SupportButton'
import { useI18n } from '../../i18n/i18n-context'
import { useSite } from '../../site/site-context'

interface AuthShellProps {
  /** 标题里品牌名前面的字，如“欢迎使用”。 */
  lead: string
  subtitle: string
  children: ReactNode
}

/** 登录、注册、找回口令共用的分屏外壳（docs/06 §5）：左侧表单，右侧全高背景与标语。 */
export function AuthShell({ lead, subtitle, children }: AuthShellProps) {
  const site = useSite()
  const { t } = useI18n()
  return (
    <div className="hub-login">
      <section className="hub-login-form">
        <h1>
          {lead} <strong>{site.siteName}</strong>
        </h1>
        <p className="hub-muted hub-login-subtitle">{subtitle}</p>
        {children}
        <div className="hub-login-footer">
          <LocaleSwitch />
        </div>
      </section>
      <section className="hub-login-art" aria-hidden="true">
        <div>
          <h2>{site.siteName}</h2>
          <p>{t('shell.slogan')}</p>
        </div>
      </section>
      <SupportButton />
    </div>
  )
}
