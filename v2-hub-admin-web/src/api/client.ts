import createClient, { type Middleware } from 'openapi-fetch'
import type { components, paths } from './generated/schema'

/** 契约里的数据结构（hub-api.yaml components.schemas）。 */
export type Session = components['schemas']['Session']
export type Admin = components['schemas']['Admin']
export type Role = components['schemas']['Role']
export type Permission = components['schemas']['Permission']
export type AuditLog = components['schemas']['AuditLog']

/** 分页结果（hub-api.yaml 各分页接口的 data 形状）。 */
export interface PageData<T> {
  items: T[]
  total: number
}

/** 统一响应信封里的错误信息。 */
export interface ApiError {
  code: string
  message: string
}

const HEADER_CSRF = 'X-CSRF-Token'
const SAFE_METHODS = new Set(['GET', 'HEAD'])
const FALLBACK_MESSAGE = '网络异常，请稍后重试'
export const CODE_UNAUTHENTICATED = 'unauthenticated'
/** 口令正确但需要两步验证码（docs/03 §4.3）。 */
export const CODE_TOTP_REQUIRED = 'totp_required'

let csrfToken = ''

/** 登录或刷新会话后由会话层写入；非 GET/HEAD 请求自动带上（docs/03 §4.2）。 */
export function setCsrfToken(token: string) {
  csrfToken = token
}

/**
 * 契约把 X-CSRF-Token 声明为写接口的必填头，生成的类型因此要求传入；
 * 实际值以中间件写入的为准，这里只是满足类型并保持与当前 token 一致。
 */
export function csrfHeader() {
  return { header: { 'X-CSRF-Token': csrfToken } }
}

const csrfMiddleware: Middleware = {
  onRequest({ request }) {
    if (!SAFE_METHODS.has(request.method) && csrfToken !== '') {
      request.headers.set(HEADER_CSRF, csrfToken)
    }
    return request
  },
}

/**
 * hub-server 的类型安全客户端。同域部署（Ruling-5），会话 Cookie 由浏览器自动携带。
 * fetch 包一层而不是直接传 globalThis.fetch，便于测试时替换全局 fetch。
 */
export const api = createClient<paths>({
  baseUrl: globalThis.location?.origin ?? '',
  credentials: 'same-origin',
  fetch: (request: Request) => globalThis.fetch(request),
})
api.use(csrfMiddleware)

/** 把 openapi-fetch 返回的 error 规整为 ApiError。 */
export function toApiError(error: unknown): ApiError {
  if (typeof error === 'object' && error !== null && 'code' in error && 'message' in error) {
    const { code, message } = error as { code: unknown; message: unknown }
    if (typeof code === 'string' && typeof message === 'string') {
      return { code, message }
    }
  }
  return { code: 'network', message: FALLBACK_MESSAGE }
}
