import { render, screen, waitFor } from '@testing-library/react'
import NProgress from 'nprogress'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { progressMiddleware } from './components/progress'
import { Modal } from './components/ui'
import { I18nProvider } from './i18n/I18nProvider'

const PROGRESS_DELAY_PAST_MS = 200

/** 让 isMotionEnabled() 返回 true：jsdom 默认没有 matchMedia。 */
function enableMotion() {
  vi.stubGlobal('matchMedia', (query: string) => ({ matches: false, media: query, addEventListener: () => undefined, removeEventListener: () => undefined }))
}

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

function renderModal(isOpen: boolean) {
  return (
    <I18nProvider initial="zh">
      <Modal title="标题" isOpen={isOpen} onClose={() => undefined}>
        正文
      </Modal>
    </I18nProvider>
  )
}

describe('模态框动效', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('不做动画时关闭即卸载', () => {
    const view = render(renderModal(true))
    expect(screen.getByRole('dialog')).toBeDefined()
    view.rerender(renderModal(false))
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('开启动效时先播退场动画、沿用原内容，播完再卸载', async () => {
    enableMotion()
    const view = render(renderModal(true))
    view.rerender(renderModal(false))
    expect(screen.getByText('正文')).toBeDefined()
    await waitFor(() => expect(screen.queryByText('正文')).toBeNull())
  })
})
