import { createContext, useContext } from 'react'

export type ToastKind = 'info' | 'success' | 'error'

export interface ToastApi {
  /** 弹出一条几秒后自动消失的提示。 */
  show: (text: string, kind?: ToastKind) => void
}

export const ToastContext = createContext<ToastApi | null>(null)

/** 读取提示接口，必须在 ToastProvider 内使用。 */
export function useToast(): ToastApi {
  const ctx = useContext(ToastContext)
  if (ctx === null) {
    throw new Error('useToast must be used within ToastProvider')
  }
  return ctx
}
