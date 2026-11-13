import { render } from '@testing-library/react'
import { RouterProvider, createMemoryRouter } from 'react-router'
import { vi } from 'vitest'
import { SessionProvider } from '../auth/SessionProvider'
import { ToastProvider } from '../components/ToastProvider'
import { translate, type Translate } from '../i18n/i18n-context'
import { I18nProvider } from '../i18n/I18nProvider'
import type { Locale } from '../i18n/locale'
import { routes } from '../routes'
import { SiteProvider } from '../site/SiteProvider'

/** 纯函数标签工具的中文 / 英文翻译函数。 */
export const zhT: Translate = (key, vars) => translate('zh', key, vars)
export const enT: Translate = (key, vars) => translate('en', key, vars)

/** 测试里统一使用的 CSRF token，断言写请求是否带头时用。 */
export const TEST_CSRF = 'csrf-test-token'

/** 成功响应信封。 */
export const ok = (data: unknown) => Response.json({ code: '0', message: 'ok', data })
/** 失败响应信封。 */
export const fail = (status: number, code: string, message: string) => Response.json({ code, message }, { status })

/** 一次被拦截的请求。 */
export interface Recorded {
  method: string
  path: string
  csrf: string | null
  body: unknown
}

/**
 * 替换全局 fetch：按 “METHOD 路径” 查表应答，没有登记的返回 404；返回请求记录供断言。
 * 调用方在 afterEach 里 vi.unstubAllGlobals()。
 */
export function mockServer(handlers: Record<string, () => Response>): Recorded[] {
  const calls: Recorded[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (req: Request) => {
      const path = new URL(req.url).pathname
      const text = await req.clone().text()
      calls.push({ method: req.method, path, csrf: req.headers.get('X-CSRF-Token'), body: text === '' ? null : JSON.parse(text) })
      const handler = handlers[`${req.method} ${path}`]
      return handler === undefined ? fail(404, 'not_found', '不存在') : handler()
    }),
  )
  return calls
}

/** 以整套 Provider（语言、提示、站点信息、会话、路由）渲染应用，从 initialPath 进入；界面语言默认中文。 */
export function renderApp(initialPath: string, locale: Locale = 'zh') {
  const router = createMemoryRouter(routes, { initialEntries: [initialPath] })
  return render(
    <I18nProvider initial={locale}>
      <ToastProvider>
        <SiteProvider>
          <SessionProvider>
            <RouterProvider router={router} />
          </SessionProvider>
        </SiteProvider>
      </ToastProvider>
    </I18nProvider>,
  )
}
