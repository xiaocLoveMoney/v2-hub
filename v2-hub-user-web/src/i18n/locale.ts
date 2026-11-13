/** 界面语言（docs/03 §5.20 中英双语，Ruling-45）：只有简体中文与英文。 */
export type Locale = 'zh' | 'en'

export const LOCALE_ZH: Locale = 'zh'
export const LOCALE_EN: Locale = 'en'
export const LOCALES: readonly Locale[] = [LOCALE_ZH, LOCALE_EN]

/** 选择记在 localStorage 的键；只是界面偏好，不属凭据。 */
export const LOCALE_STORAGE_KEY = 'hub.locale'
const CHINESE_PREFIX = 'zh'

/** 每种语言给日期、数字与金额格式化用的语言标签。 */
export const LOCALE_TAG: Record<Locale, string> = { zh: 'zh-CN', en: 'en-US' }

/** 切换按钮上显示的语言名（用该语言自己的写法，不随界面语言变）。 */
export const LOCALE_NAME: Record<Locale, string> = { zh: '中文', en: 'English' }

function isLocale(value: unknown): value is Locale {
  return value === LOCALE_ZH || value === LOCALE_EN
}

/** 读取用户上次的选择；没有、读不到（隐私模式等）或不认识时返回 null。 */
export function loadSavedLocale(): Locale | null {
  try {
    const saved = globalThis.localStorage?.getItem(LOCALE_STORAGE_KEY)
    return isLocale(saved) ? saved : null
  } catch {
    // 存储不可用只影响“记住选择”，按浏览器语言处理即可
    return null
  }
}

/** 记住选择；写不进去时只是下次不记得，不影响当前界面。 */
export function saveLocale(locale: Locale) {
  try {
    globalThis.localStorage?.setItem(LOCALE_STORAGE_KEY, locale)
  } catch {
    // 同上：偏好写不进去不算错误
  }
}

/** 浏览器语言以 zh 开头用中文，其余用英文。 */
export function detectLocale(language: string | undefined): Locale {
  return language?.toLowerCase().startsWith(CHINESE_PREFIX) === true ? LOCALE_ZH : LOCALE_EN
}

/** 首次进入时的语言：先看上次选择，再看浏览器语言。 */
export function initialLocale(): Locale {
  return loadSavedLocale() ?? detectLocale(globalThis.navigator?.language)
}
