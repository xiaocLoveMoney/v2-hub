import NProgress from 'nprogress'
import type { Middleware } from 'openapi-fetch'

/**
 * 顶部请求进度条（NProgress）：以 openapi-fetch 中间件接在 api 客户端上，任何接口请求在途时显示，全部结束后收起。
 * 请求在 SHOW_DELAY 内就结束则不显示，避免快速请求造成闪烁。样式在 styles/components.css，使用设计变量。
 */
const PROGRESS_SHOW_DELAY_MS = 150
const PROGRESS_MINIMUM = 0.12
const PROGRESS_SPEED_MS = 300
const PROGRESS_TRICKLE_SPEED_MS = 200

NProgress.configure({ showSpinner: false, minimum: PROGRESS_MINIMUM, speed: PROGRESS_SPEED_MS, trickleSpeed: PROGRESS_TRICKLE_SPEED_MS })

let pendingCount = 0
let showTimer: ReturnType<typeof setTimeout> | undefined

function beginRequest() {
  pendingCount += 1
  if (pendingCount > 1 || NProgress.isStarted()) {
    return
  }
  showTimer = setTimeout(() => {
    showTimer = undefined
    NProgress.start()
  }, PROGRESS_SHOW_DELAY_MS)
}

function endRequest() {
  pendingCount = Math.max(pendingCount - 1, 0)
  if (pendingCount > 0) {
    return
  }
  if (showTimer !== undefined) {
    clearTimeout(showTimer)
    showTimer = undefined
    return
  }
  NProgress.done()
}

/**
 * 计数用的中间件：onRequest 记一次，onResponse / onError 各消掉一次。
 * openapi-fetch 对每个请求只会走 onResponse 与 onError 之一（含取消请求抛出的 AbortError），计数不会漏减。
 */
export const progressMiddleware: Middleware = {
  onRequest() {
    beginRequest()
    return undefined
  },
  onResponse() {
    endRequest()
    return undefined
  },
  onError() {
    endRequest()
    return undefined
  },
}
