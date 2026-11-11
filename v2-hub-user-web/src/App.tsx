import { useEffect, useState } from 'react'
import { fetchHealth } from './api/health'
import { APP_TITLE } from './app-config'

/** 后端探活状态。 */
type BackendState = 'checking' | 'up' | 'down'

const BACKEND_STATE_TEXT: Record<BackendState, string> = {
  checking: '检测中…',
  up: '后端在线',
  down: '后端不可用',
}

/** 应用根组件：脚手架阶段只展示标题与后端探活结果，菜单与路由在 M0 登录任务中补。 */
export default function App() {
  const [backendState, setBackendState] = useState<BackendState>('checking')

  useEffect(() => {
    const controller = new AbortController()
    const probe = async () => {
      try {
        const isUp = await fetchHealth(controller.signal)
        setBackendState(isUp ? 'up' : 'down')
      } catch (err) {
        // 卸载时主动取消属于正常流程，不更新状态；其余错误视为后端不可用
        if (controller.signal.aborted) {
          return
        }
        setBackendState('down')
        reportError(err)
      }
    }
    void probe()
    return () => controller.abort()
  }, [])

  return (
    <main className="app">
      <h1>{APP_TITLE}</h1>
      <p data-testid="backend-state">{BACKEND_STATE_TEXT[backendState]}</p>
    </main>
  )
}
