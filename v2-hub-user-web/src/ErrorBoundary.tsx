import { Component, type ErrorInfo, type ReactNode } from 'react'
import { translate } from './i18n/i18n-context'
import { initialLocale } from './i18n/locale'

interface ErrorBoundaryProps {
  children: ReactNode
}

interface ErrorBoundaryState {
  hasError: boolean
}

/** 顶层错误边界：渲染异常时展示友好文案，不透出堆栈。 */
export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { hasError: false }

  static getDerivedStateFromError(): ErrorBoundaryState {
    return { hasError: true }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    reportError(Object.assign(error, { componentStack: info.componentStack }))
  }

  render() {
    if (this.state.hasError) {
      // 错误边界在语言 Provider 外面，直接按上次选择 / 浏览器语言取词条
      return <p role="alert">{translate(initialLocale(), 'common.renderFailed')}</p>
    }
    return this.props.children
  }
}
