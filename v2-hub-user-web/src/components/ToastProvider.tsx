import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { EASE_LEAVE, EASE_POP, MOTION_BASE, MOTION_FAST, gsap, isMotionEnabled, useGSAP } from './motion'
import { ToastContext, type ToastApi, type ToastKind } from './toast-context'

const TOAST_DURATION_MS = 2500
/** 进场时自上而下移动的距离（px）。 */
const TOAST_OFFSET_PX = 12
/** 与 .hub-toasts 的 gap 一致：退场时把这段间距一起收掉，下面的提示平滑上移。 */
const TOAST_GAP_PX = 8

interface ToastItem {
  id: number
  text: string
  kind: ToastKind
  /** 已到时、正在播退场动画。 */
  isLeaving: boolean
}

interface ToastViewProps {
  item: ToastItem
  onExited: (id: number) => void
}

/** 单条提示：挂载时下落淡入；isLeaving 后淡出并收起高度，播完通知父组件移除。 */
function ToastView({ item, onExited }: ToastViewProps) {
  const ref = useRef<HTMLDivElement>(null)
  useGSAP(
    () => {
      const el = ref.current
      if (el === null || !isMotionEnabled()) {
        return
      }
      if (!item.isLeaving) {
        gsap.fromTo(el, { autoAlpha: 0, y: -TOAST_OFFSET_PX }, { autoAlpha: 1, y: 0, duration: MOTION_BASE, ease: EASE_POP })
        return
      }
      gsap.to(el, {
        autoAlpha: 0,
        y: -TOAST_OFFSET_PX,
        height: 0,
        paddingTop: 0,
        paddingBottom: 0,
        marginBottom: -TOAST_GAP_PX,
        duration: MOTION_FAST,
        ease: EASE_LEAVE,
        overwrite: true,
        onComplete: () => onExited(item.id),
      })
    },
    { dependencies: [item.isLeaving], scope: ref },
  )
  return (
    <div ref={ref} className={`hub-toast${item.kind === 'info' ? '' : ` hub-toast-${item.kind}`}`}>
      {item.text}
    </div>
  )
}

/** 全局提示：成功、失败、复制结果等统一走这里（docs/06 §3 不变式 5）。不做动画时（见 motion.ts）到时直接移除。 */
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

  const remove = useCallback((id: number) => {
    setItems((prev) => prev.filter((t) => t.id !== id))
  }, [])

  const show = useCallback(
    (text: string, kind: ToastKind = 'info') => {
      const id = nextId.current
      nextId.current += 1
      setItems((prev) => [...prev, { id, text, kind, isLeaving: false }])
      const timer = setTimeout(() => {
        timers.current.delete(timer)
        if (!isMotionEnabled()) {
          remove(id)
          return
        }
        setItems((prev) => prev.map((t) => (t.id === id ? { ...t, isLeaving: true } : t)))
      }, TOAST_DURATION_MS)
      timers.current.add(timer)
    },
    [remove],
  )

  const api = useMemo<ToastApi>(() => ({ show }), [show])
  return (
    <ToastContext.Provider value={api}>
      {children}
      <div className="hub-toasts" role="status" aria-live="polite">
        {items.map((t) => (
          <ToastView key={t.id} item={t} onExited={remove} />
        ))}
      </div>
    </ToastContext.Provider>
  )
}
