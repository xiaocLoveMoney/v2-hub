import createClient, { type Middleware } from 'openapi-fetch'
import type { components, paths } from './generated/schema'

/** 契约里的数据结构（hub-api.yaml components.schemas）。 */
export type UserSession = components['schemas']['UserSession']
export type UserStatus = components['schemas']['UserStatus']
export type Dashboard = components['schemas']['UserDashboard']
export type TrafficPoint = components['schemas']['TrafficPoint']
export type UserNode = components['schemas']['UserNode']
export type UserNodeGroup = components['schemas']['UserNodeGroup']
export type SubscriptionInfo = components['schemas']['SubscriptionInfo']
export type SubscriptionFormat = components['schemas']['SubscriptionFormat']
export type LoginLog = components['schemas']['LoginLog']
export type SubscribeLog = components['schemas']['SubscribeLog']
export type TotpSetup = components['schemas']['TotpSetup']
export type SiteInfo = components['schemas']['SiteInfo']
export type RegisterInput = components['schemas']['RegisterInput']
export type InviteInfo = components['schemas']['InviteInfo']
export type Announcement = components['schemas']['Announcement']
export type Tutorial = components['schemas']['Tutorial']
export type TutorialPlatform = components['schemas']['TutorialPlatform']
export type SharedAccount = components['schemas']['SharedAccount']
export type Ticket = components['schemas']['Ticket']
export type TicketDetail = components['schemas']['TicketDetail']
export type TicketMessage = components['schemas']['TicketMessage']
export type TicketStatus = components['schemas']['TicketStatus']
export type CheckinInfo = components['schemas']['CheckinInfo']
export type Shop = components['schemas']['Shop']
export type ShopPlan = components['schemas']['ShopPlan']
export type ShopTrafficPack = components['schemas']['ShopTrafficPack']
export type PlanPrice = components['schemas']['PlanPrice']
export type OrderInput = components['schemas']['OrderInput']
export type Quote = components['schemas']['Quote']
export type Order = components['schemas']['Order']
export type Wallet = components['schemas']['Wallet']
export type LedgerEntry = components['schemas']['LedgerEntry']
export type LedgerKind = components['schemas']['LedgerKind']
export type UserFeatures = NonNullable<UserSession['features']>
export type ChainOverview = components['schemas']['ChainOverview']
export type ChainRule = components['schemas']['ChainRule']
export type ChainNodeOption = components['schemas']['ChainNodeOption']
export type ChainBlockedReason = components['schemas']['ChainBlockedReason']
export type CustomNode = components['schemas']['CustomNode']

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
/** 口令正确但需要两步验证码（docs/03 §5.3）。 */
export const CODE_TOTP_REQUIRED = 'totp_required'
/** 账号已停用，只能查看本人信息与退出（docs/03 §5.3）。 */
export const CODE_ACCOUNT_DISABLED = 'account_disabled'
/** 该功能未开启（注册关闭、签到 / 工单未开、邮件未配置等；docs/03 §4 错误码）。 */
export const CODE_FEATURE_DISABLED = 'feature_disabled'
/** 余额不足以支付本次下单；data.shortfall 是差额（分）（docs/03 §5.13）。 */
export const CODE_INSUFFICIENT_BALANCE = 'insufficient_balance'

let csrfToken = ''

/** 登录或刷新会话后由会话层写入；非 GET/HEAD 请求自动带上（docs/03 §5.3）。 */
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
