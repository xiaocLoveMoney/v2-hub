import { RouterProvider, createBrowserRouter } from 'react-router'
import { SessionProvider } from './auth/SessionProvider'
import { ToastProvider } from './components/ToastProvider'
import { routes } from './routes'

const router = createBrowserRouter(routes)

/** 应用根组件：全局提示、会话上下文、路由。 */
export default function App() {
  return (
    <ToastProvider>
      <SessionProvider>
        <RouterProvider router={router} />
      </SessionProvider>
    </ToastProvider>
  )
}
