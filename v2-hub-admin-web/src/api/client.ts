import createClient, { type Middleware } from 'openapi-fetch'
import type { components, paths } from './generated/schema'

/** 契约里的数据结构（hub-api.yaml components.schemas）。 */
export type Session = components['schemas']['Session']
export type Admin = components['schemas']['Admin']
export type Role = components['schemas']['Role']
export type Permission = components['schemas']['Permission']
export type AuditLog = components['schemas']['AuditLog']
export type Server = components['schemas']['Server']
export type ServerBillingInput = components['schemas']['ServerBillingInput']
export type ServerTrend = components['schemas']['ServerTrend']
export type ServerMetrics = components['schemas']['ServerMetrics']
export type LatencySeries = components['schemas']['LatencySeries']
export type LatencyPoint = components['schemas']['LatencyPoint']
export type Inbound = components['schemas']['Inbound']
export type Endpoint = components['schemas']['Endpoint']
export type PanelNode = components['schemas']['Node']
export type NodeGroup = components['schemas']['NodeGroup']
export type TlsMode = components['schemas']['TlsMode']
export type ProviderType = components['schemas']['ServerConnInput']['providerType']
export type Plan = components['schemas']['Plan']
export type PlanInput = components['schemas']['PlanInput']
export type EndUser = components['schemas']['User']
export type UserStatus = components['schemas']['UserStatus']
export type TrafficReset = components['schemas']['TrafficReset']
export type Provision = components['schemas']['Provision']
export type Settings = components['schemas']['Settings']
export type RegisterMode = components['schemas']['RegisterMode']
export type MailOutboxItem = components['schemas']['MailOutboxItem']
export type MailStatus = components['schemas']['MailStatus']
export type InviteCode = components['schemas']['InviteCode']
export type InviteCodeInput = components['schemas']['InviteCodeInput']
export type Announcement = components['schemas']['Announcement']
export type AnnouncementInput = components['schemas']['AnnouncementInput']
export type Tutorial = components['schemas']['Tutorial']
export type TutorialInput = components['schemas']['TutorialInput']
export type TutorialPlatform = components['schemas']['TutorialPlatform']
export type SharedAccount = components['schemas']['SharedAccount']
export type SharedAccountInput = components['schemas']['SharedAccountInput']
export type Ticket = components['schemas']['Ticket']
export type TicketDetail = components['schemas']['TicketDetail']
export type TicketMessage = components['schemas']['TicketMessage']
export type TicketStatus = components['schemas']['TicketStatus']
export type TrafficPack = components['schemas']['TrafficPack']
export type TrafficPackInput = components['schemas']['TrafficPackInput']
export type Coupon = components['schemas']['Coupon']
export type CouponInput = components['schemas']['CouponInput']
export type CouponKind = components['schemas']['CouponKind']
export type TopupCode = components['schemas']['TopupCode']
export type TopupCodeStatus = components['schemas']['TopupCodeStatus']
export type Order = components['schemas']['Order']
export type OrderKind = components['schemas']['OrderKind']
export type LedgerEntry = components['schemas']['LedgerEntry']
export type LedgerKind = components['schemas']['LedgerKind']
export type Payment = components['schemas']['Payment']
export type PaymentStatus = components['schemas']['PaymentStatus']
export type Rebate = components['schemas']['Rebate']
export type FinanceStats = components['schemas']['FinanceStats']
export type FinanceGranularity = components['schemas']['FinanceGranularity']
export type AdminDashboard = components['schemas']['AdminDashboard']
export type AnalyticsUsers = components['schemas']['AnalyticsUsers']
export type AnalyticsTraffic = components['schemas']['AnalyticsTraffic']
export type AnalyticsServers = components['schemas']['AnalyticsServers']
export type AnalyticsServerLoad = components['schemas']['AnalyticsServerLoad']
export type AnalyticsFinance = components['schemas']['AnalyticsFinance']
export type AnalyticsBacklog = components['schemas']['AnalyticsBacklog']
export type MetricRange = components['schemas']['MetricRange']
export type ReachVerdict = components['schemas']['ReachVerdict']
export type ReachScope = components['schemas']['ReachScope']
export type ReachProblem = components['schemas']['ReachProblem']
export type ReachProbe = components['schemas']['ReachProbe']
export type NodeReach = components['schemas']['NodeReach']
export type NodeReachDetail = components['schemas']['NodeReachDetail']
export type AdminKind = components['schemas']['AdminKind']
export type Agent = components['schemas']['Agent']
export type AgentCreateInput = components['schemas']['AgentCreateInput']
export type AgentUpdateInput = components['schemas']['AgentUpdateInput']
export type AgentBrief = components['schemas']['AgentBrief']
export type AgentAllocation = components['schemas']['AgentAllocation']
export type AgentAllocationKind = components['schemas']['AgentAllocationKind']
export type AgentSummary = components['schemas']['AgentSummary']
export type AgentPlan = components['schemas']['AgentPlan']
export type AgentUser = components['schemas']['AgentUser']
export type AgentUserQuotaInput = components['schemas']['AgentUserQuotaInput']

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
/** 密码正确但需要两步验证码（docs/03 §4.3）。 */
export const CODE_TOTP_REQUIRED = 'totp_required'
/** 高危操作须先做二次验证（R12，Ruling-100）。 */
export const CODE_REAUTH_REQUIRED = 'reauth_required'
/** 超级管理员尚未开启两步验证（R10，Ruling-98）。 */
export const CODE_TOTP_SETUP_REQUIRED = 'totp_setup_required'
/** 频控触发（docs/03 §5）。 */
export const CODE_RATE_LIMITED = 'rate_limited'
/** 代理的流量池不够本次开通，信封 data 带 poolCost / poolRemaining（docs/03 §5.23）。 */
export const CODE_AGENT_POOL_INSUFFICIENT = 'agent_pool_insufficient'
/** 代理名下用户数已达上限（docs/03 §5.23）。 */
export const CODE_AGENT_USER_LIMIT = 'agent_user_limit'
const HTTP_FORBIDDEN = 403
/** 会话与验证类接口：本身不会要求二次验证，也不能被拦截（否则验证请求会递归弹框）。 */
const AUTH_PATH_PREFIX = '/api/admin/auth/'

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

/** 二次验证处理器：弹出验证框，验证通过返回 true，用户取消返回 false。 */
export type ReauthHandler = () => Promise<boolean>

let reauthHandler: ReauthHandler | null = null
/** 进行中的那一次验证；并发遇到 reauth_required 的请求共用它，只弹一次框。 */
let pendingReauth: Promise<boolean> | null = null
let totpSetupHandler: (() => void) | null = null

/**
 * 登记二次验证处理器（由 ReauthProvider 挂载时调用，Ruling-100）。
 * @param handler 弹框并返回是否验证通过
 * @returns 注销函数；只注销自己登记的那一个
 */
export function registerReauthHandler(handler: ReauthHandler): () => void {
  reauthHandler = handler
  return () => {
    if (reauthHandler === handler) {
      reauthHandler = null
    }
  }
}

/**
 * 登记“超管须先开启两步验证”的处理器（由 SessionProvider 挂载时调用，用来刷新会话，Ruling-98）。
 * @param handler 收到 totp_setup_required 时调用
 * @returns 注销函数；只注销自己登记的那一个
 */
export function registerTotpSetupHandler(handler: () => void): () => void {
  totpSetupHandler = handler
  return () => {
    if (totpSetupHandler === handler) {
      totpSetupHandler = null
    }
  }
}

/** 读出 403 响应信封里的错误码；读不出（非 JSON 等）返回空串。读的是副本，原响应的 body 不受影响。 */
async function peekErrorCode(response: Response): Promise<string> {
  try {
    const envelope: unknown = await response.clone().json()
    if (typeof envelope === 'object' && envelope !== null && 'code' in envelope && typeof envelope.code === 'string') {
      return envelope.code
    }
  } catch (err) {
    // 403 却不是 JSON 信封（如网关页）：按普通 403 交还调用方
    reportError(err)
  }
  return ''
}

/** 发起（或加入进行中的）一次二次验证。 */
function requestReauth(handler: ReauthHandler): Promise<boolean> {
  if (pendingReauth === null) {
    pendingReauth = handler().finally(() => {
      pendingReauth = null
    })
  }
  return pendingReauth
}

/**
 * 所有接口请求的唯一出口：403 reauth_required 时弹验证框，通过后用发请求前留底的副本原样重发一次
 * （方法、URL、请求体、CSRF 头都不变；重发结果直接交还，不再拦截，避免循环）；取消则把原 403 交还调用方（Ruling-100）。
 * 403 totp_setup_required 时通知会话层刷新（Ruling-98）。/api/admin/auth/ 下的请求不拦截。
 */
async function guardedFetch(request: Request): Promise<Response> {
  if (new URL(request.url).pathname.startsWith(AUTH_PATH_PREFIX)) {
    return globalThis.fetch(request)
  }
  const replica = request.clone()
  const response = await globalThis.fetch(request)
  if (response.status !== HTTP_FORBIDDEN) {
    return response
  }
  const code = await peekErrorCode(response)
  if (code === CODE_TOTP_SETUP_REQUIRED) {
    totpSetupHandler?.()
    return response
  }
  const handler = reauthHandler
  if (code !== CODE_REAUTH_REQUIRED || handler === null) {
    return response
  }
  const isPassed = await requestReauth(handler)
  return isPassed ? globalThis.fetch(replica) : response
}

/**
 * hub-server 的类型安全客户端。同域部署（Ruling-5），会话 Cookie 由浏览器自动携带。
 * fetch 包一层而不是直接传 globalThis.fetch，便于测试时替换全局 fetch，也是二次验证拦截的位置。
 */
export const api = createClient<paths>({
  baseUrl: globalThis.location?.origin ?? '',
  credentials: 'same-origin',
  fetch: guardedFetch,
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
export type SubscribeLog = components['schemas']['SubscribeLog']
export type ChainApply = components['schemas']['ChainApply']
export type ChainApplyStatus = components['schemas']['ChainApplyStatus']
export type AdminChainRoute = components['schemas']['AdminChainRoute']
export type AdminChainRouteEntry = components['schemas']['AdminChainRouteEntry']
export type ChainKind = components['schemas']['ChainKind']
export type ChainEntryState = components['schemas']['ChainEntryState']
export type ChainExitKind = components['schemas']['ChainExitKind']
export type ChainBlockedReason = components['schemas']['ChainBlockedReason']
export type AdminChainExit = components['schemas']['AdminChainExit']
export type ChainExitTestMode = components['schemas']['ChainExitTestMode']
export type AdminChainExitTestResult = components['schemas']['AdminChainExitTestResult']
export type ServerWebCert = components['schemas']['ServerWebCert']
export type RealityScanResult = components['schemas']['RealityScanResult']
export type AdminChainExitInput = components['schemas']['AdminChainExitInput']
export type ChainExitOwner = components['schemas']['ChainExitOwner']
export type ChainExitAssignMode = components['schemas']['ChainExitAssignMode']
export type SshCredential = components['schemas']['SshCredential']
export type SshCredentialInput = components['schemas']['SshCredentialInput']
export type SshAuthType = components['schemas']['SshAuthType']
export type DnsAccount = components['schemas']['DnsAccount']
export type DnsAccountInput = components['schemas']['DnsAccountInput']
export type DnsProviderType = components['schemas']['DnsProviderType']
export type ServerHost = components['schemas']['ServerHost']
export type ServerHostBatchInput = components['schemas']['ServerHostBatchInput']
export type HostDetection = components['schemas']['HostDetection']
export type HostTask = components['schemas']['HostTask']
export type HostTaskDetail = components['schemas']['HostTaskDetail']
export type HostTaskKind = components['schemas']['HostTaskKind']
export type HostTaskStatus = components['schemas']['HostTaskStatus']
export type HostErrorCode = components['schemas']['HostErrorCode']
export type HostAction = components['schemas']['HostActionInput']['action']
export type PanelLogin = components['schemas']['PanelLogin']
export type ClientFlow = components['schemas']['ClientFlow']
export type InboundPresetKey = components['schemas']['InboundPresetKey']
export type InboundPresetPreview = components['schemas']['InboundPresetPreview']
export type InboundPresetDeployInput = components['schemas']['InboundPresetDeployInput']
