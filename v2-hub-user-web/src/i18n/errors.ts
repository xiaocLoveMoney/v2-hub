import type { ApiError } from '../api/client'
import { translate, type MessageKey } from './i18n-context'
import { LOCALE_ZH, type Locale } from './locale'

/** 后端错误码（docs/03 §4）与前端的 network 对应的通用说明词条。 */
const ERROR_KEYS: Record<string, MessageKey> = {
  invalid_argument: 'error.invalidArgument',
  unauthenticated: 'error.unauthenticated',
  permission_denied: 'error.permissionDenied',
  not_found: 'error.notFound',
  conflict: 'error.conflict',
  rate_limited: 'error.rateLimited',
  totp_required: 'error.totpRequired',
  upstream_unavailable: 'error.upstreamUnavailable',
  account_disabled: 'error.accountDisabled',
  feature_disabled: 'error.featureDisabled',
  insufficient_balance: 'error.insufficientBalance',
  internal: 'error.internal',
  network: 'error.network',
}

/** 这些错误码的说明是前端自己写的中文兜底，英文界面不必再附在后面。 */
const FRONTEND_CODES = new Set(['network'])

/**
 * 错误说明（Ruling-45）：中文界面原样用后端说明；英文界面按错误码给通用英文说明，
 * 后端的中文说明放在括号里附在后面，未知错误码用“请求失败”。
 */
export function describeError(locale: Locale, error: ApiError): string {
  if (locale === LOCALE_ZH) {
    return error.message
  }
  const generic = translate(locale, ERROR_KEYS[error.code] ?? 'error.generic')
  if (error.message === '' || FRONTEND_CODES.has(error.code)) {
    return generic
  }
  return `${generic} (${error.message})`
}
