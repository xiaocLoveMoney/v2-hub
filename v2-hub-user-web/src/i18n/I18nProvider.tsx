import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { setErrorTranslator } from '../api/client'
import { describeError } from './errors'
import { I18nContext, makeI18n } from './i18n-context'
import { LOCALE_TAG, initialLocale, saveLocale, type Locale } from './locale'

interface I18nProviderProps {
  /** 指定初始语言（测试用）；不传则按上次选择与浏览器语言决定。 */
  initial?: Locale
  children: ReactNode
}

/**
 * 界面语言（docs/06 §5 M8 的做法）：决定词条、格式化用的语言标签与 `<html lang>`，
 * 并让接口层的错误说明按当前语言给出。
 */
export function I18nProvider({ initial, children }: I18nProviderProps) {
  const [locale, setLocaleState] = useState<Locale>(() => initial ?? initialLocale())

  const setLocale = useCallback((next: Locale) => {
    saveLocale(next)
    setLocaleState(next)
  }, [])

  useEffect(() => {
    document.documentElement.lang = LOCALE_TAG[locale]
    setErrorTranslator((err) => describeError(locale, err))
  }, [locale])

  const value = useMemo(() => makeI18n(locale, setLocale), [locale, setLocale])
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>
}
