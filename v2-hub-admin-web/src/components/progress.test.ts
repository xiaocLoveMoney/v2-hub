import NProgress from 'nprogress'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { progressMiddleware } from './progress'

const PROGRESS_DELAY_PAST_MS = 200

/** 直接调用中间件的三个钩子；参数只用到形状，这里给最小值。 */
type Hook = (arg: never) => unknown
const callHook = (hook: unknown) => (hook as Hook)({} as never)

describe('请求进度条', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('快速结束的请求不显示进度条', () => {
    vi.useFakeTimers()
    const start = vi.spyOn(NProgress, 'start')
    callHook(progressMiddleware.onRequest)
    callHook(progressMiddleware.onResponse)
    vi.advanceTimersByTime(PROGRESS_DELAY_PAST_MS)
    expect(start).not.toHaveBeenCalled()
  })

  it('慢请求显示进度条，并发请求全部结束（含失败）后才收起', () => {
    vi.useFakeTimers()
    const start = vi.spyOn(NProgress, 'start').mockImplementation(() => NProgress)
    const done = vi.spyOn(NProgress, 'done').mockImplementation(() => NProgress)
    callHook(progressMiddleware.onRequest)
    callHook(progressMiddleware.onRequest)
    vi.advanceTimersByTime(PROGRESS_DELAY_PAST_MS)
    expect(start).toHaveBeenCalledTimes(1)
    callHook(progressMiddleware.onResponse)
    expect(done).not.toHaveBeenCalled()
    callHook(progressMiddleware.onError)
    expect(done).toHaveBeenCalledTimes(1)
  })
})
