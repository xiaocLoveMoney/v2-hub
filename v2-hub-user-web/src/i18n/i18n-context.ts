import { Fragment, createContext, createElement, useContext, type ReactNode } from 'react'
import { LOCALE_TAG, LOCALE_ZH, type Locale } from './locale'
import { en } from './messages/en'
import { zh, type MessageKey } from './messages/zh'

/** 占位符写法 `{name}`；变量名只含字母数字。 */
const PLACEHOLDER = /\{(\w+)\}/g
/** 按占位符切分时保留占位符本身（捕获组），用于拼 React 节点。 */
const PLACEHOLDER_SPLIT = /(\{\w+\})/
const PLACEHOLDER_EDGE_LEN = 1

export type { MessageKey }
export type MessageVars = Record<string, string | number>

const DICTIONARIES: Record<Locale, Record<MessageKey, string>> = { zh, en }

/** 按语言取词条并替换占位符；变量缺失时保留占位符原文，便于发现漏传。 */
export function translate(locale: Locale, key: MessageKey, vars?: MessageVars): string {
  const text = DICTIONARIES[locale][key]
  if (vars === undefined) {
    return text
  }
  return text.replace(PLACEHOLDER, (whole, name: string) => (name in vars ? String(vars[name]) : whole))
}

/** 翻译函数的形状；纯函数的标签工具以它为参数，便于测试时直接传入。 */
export type Translate = (key: MessageKey, vars?: MessageVars) => string

const SINGULAR = 1

/** 按数量选单复数词条（英文 1 year / 2 years；中文两条相同），变量名固定为 n。 */
export function plural(t: Translate, n: number, one: MessageKey, other: MessageKey): string {
  return t(n === SINGULAR ? one : other, { n })
}

/** 当前语言的界面工具。 */
export interface I18n {
  locale: Locale
  /** 日期、数字、金额格式化用的语言标签，如 zh-CN。 */
  tag: string
  setLocale: (locale: Locale) => void
  /** 取词条文字。 */
  t: Translate
  /** 取词条并把占位符换成 React 节点（加粗用户名等）；返回可直接渲染的数组。 */
  rich: (key: MessageKey, nodes: Record<string, ReactNode>) => ReactNode[]
}

/** 按语言造一份 I18n；Provider 与测试共用。 */
export function makeI18n(locale: Locale, setLocale: (locale: Locale) => void): I18n {
  return {
    locale,
    tag: LOCALE_TAG[locale],
    setLocale,
    t: (key, vars) => translate(locale, key, vars),
    // 切出的片段顺序固定、不增删，用下标作 key 没有重排问题
    rich: (key, nodes) =>
      DICTIONARIES[locale][key].split(PLACEHOLDER_SPLIT).map((part, index) => {
        const name = part.slice(PLACEHOLDER_EDGE_LEN, -PLACEHOLDER_EDGE_LEN)
        return createElement(Fragment, { key: index }, part.startsWith('{') && name in nodes ? nodes[name] : part)
      }),
  }
}

const noop = () => undefined

export const I18nContext = createContext<I18n>(makeI18n(LOCALE_ZH, noop))

/** 读取当前语言与翻译函数。 */
export function useI18n(): I18n {
  return useContext(I18nContext)
}
