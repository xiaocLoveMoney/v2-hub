import { Languages } from 'lucide-react'
import { useI18n } from '../i18n/i18n-context'
import { LOCALES, LOCALE_NAME } from '../i18n/locale'

const ICON_SIZE = 14

/** 页脚的语言切换（docs/03 §5.20 中英双语）：当前语言高亮，点另一种即切换并记住。 */
export function LocaleSwitch() {
  const { locale, setLocale, t } = useI18n()
  return (
    <span className="hub-locale-switch" role="group" aria-label={t('locale.switch')}>
      <Languages size={ICON_SIZE} aria-hidden="true" />
      {LOCALES.map((l) => (
        <button key={l} type="button" lang={l} aria-pressed={l === locale} className={l === locale ? 'is-active' : ''} onClick={() => setLocale(l)}>
          {LOCALE_NAME[l]}
        </button>
      ))}
    </span>
  )
}
