import { Alert } from 'antd'
import type { ApiError } from '../api/client'

interface ErrorAlertProps {
  /** 为 null 时不渲染。 */
  error: ApiError | null
}

/** 页面或弹窗顶部的接口错误提示。 */
export function ErrorAlert({ error }: ErrorAlertProps) {
  return error === null ? null : <Alert type="error" title={error.message} showIcon className="page-alert" />
}
