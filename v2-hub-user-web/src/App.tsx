import { RouterProvider, createBrowserRouter } from 'react-router'
import { SessionProvider } from './auth/SessionProvider'
import { ToastProvider } from './components/ToastProvider'
import { routes } from './routes'
import { SiteProvider } from './site/SiteProvider'

const router = createBrowserRouter(routes)

/** 应用根组件：全局提示、站点信息、会话上下文、路由。 */
export default function App() {
  return (
    <ToastProvider>
      <SiteProvider>
        <SessionProvider>
          <RouterProvider router={router} />
        </SessionProvider>
      </SiteProvider>
    </ToastProvider>
  )
}
