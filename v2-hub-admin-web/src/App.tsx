import { App as AntApp, ConfigProvider } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import { RouterProvider, createBrowserRouter } from 'react-router'
import { SessionProvider } from './auth/SessionProvider'
import { BrandMark } from './components/BrandMark'
import { routes } from './routes'
import { adminTheme } from './theme'

/** 路由前缀跟随构建的 base（生产为 /admin/，Ruling-61）；react-router 要求不带结尾斜杠。 */
const ROUTER_BASENAME = import.meta.env.BASE_URL.replace(/\/$/, '')
const router = createBrowserRouter(routes, { basename: ROUTER_BASENAME === '' ? undefined : ROUTER_BASENAME })
/** antd 的加载指示器统一换成平台图标的步进旋转（docs/05，docs/06 §4 图标动效）；尺寸随 antd 的字号。 */
const SPIN_CONFIG = { indicator: <BrandMark motion="spin" /> }

/** 应用根组件：主题与中文语言包、会话上下文、路由。 */
export default function App() {
  return (
    <ConfigProvider locale={zhCN} theme={adminTheme} spin={SPIN_CONFIG}>
      <AntApp>
        <SessionProvider>
          <RouterProvider router={router} />
        </SessionProvider>
      </AntApp>
    </ConfigProvider>
  )
}
