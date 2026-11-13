import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { ToastContext, type ToastApi, type ToastKind } from './toast-context'

const TOAST_DURATION_MS = 2500

interface ToastItem {
  id: number
  text: string
  kind: ToastKind
}

/** 全局提示：成功、失败、复制结果等统一走这里（docs/06 §3 不变式 5）。 */
export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([])
  const nextId = useRef(1)
  const timers = useRef(new Set<ReturnType<typeof setTimeout>>())

  useEffect(() => {
    const pending = timers.current
    return () => {
      for (const t of pending) {
        clearTimeout(t)
      }
      pending.clear()
    }
  }, [])

  const show = useCallback((text: string, kind: ToastKind = 'info') => {
    const id = nextId.current
    nextId.current += 1
    setItems((prev) => [...prev, { id, text, kind }])
    const timer = setTimeout(() => {
      timers.current.delete(timer)
      setItems((prev) => prev.filter((t) => t.id !== id))
    }, TOAST_DURATION_MS)
    timers.current.add(timer)
  }, [])

  const api = useMemo<ToastApi>(() => ({ show }), [show])
  return (
    <ToastContext.Provider value={api}>
      {children}
      <div className="hub-toasts" role="status" aria-live="polite">
        {items.map((t) => (
          <div key={t.id} className={`hub-toast${t.kind === 'info' ? '' : ` hub-toast-${t.kind}`}`}>
            {t.text}
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  )
}
