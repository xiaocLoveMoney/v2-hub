import { App as AntApp, ConfigProvider } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import { RouterProvider, createBrowserRouter } from 'react-router'
import { SessionProvider } from './auth/SessionProvider'
import { routes } from './routes'
import { adminTheme } from './theme'

const router = createBrowserRouter(routes)

/** 应用根组件：主题与中文语言包、会话上下文、路由。 */
export default function App() {
  return (
    <ConfigProvider locale={zhCN} theme={adminTheme}>
      <AntApp>
        <SessionProvider>
          <RouterProvider router={router} />
        </SessionProvider>
      </AntApp>
    </ConfigProvider>
  )
}
