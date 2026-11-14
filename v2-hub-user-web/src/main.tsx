import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'
import { api } from './api/client'
import { progressMiddleware } from './components/progress'
import { ErrorBoundary } from './ErrorBoundary'
import '@fontsource-variable/nunito'
import './index.css'

const ROOT_ELEMENT_ID = 'root'

// 顶部请求进度条：在入口注册一次，避免热更新重复挂中间件
api.use(progressMiddleware)

const rootElement = document.getElementById(ROOT_ELEMENT_ID)
if (!rootElement) {
  throw new Error(`missing #${ROOT_ELEMENT_ID} element`)
}

createRoot(rootElement).render(
  <StrictMode>
    <ErrorBoundary>
      <App />
    </ErrorBoundary>
  </StrictMode>,
)
