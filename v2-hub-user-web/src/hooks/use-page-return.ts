import { useEffect, useRef } from 'react'

/** 两次触发的最小间隔：标签页可见与窗口聚焦常成对出现，来回切换时也不连发请求（docs/06 §3 第 6 条）。 */
export const MIN_PAGE_RETURN_INTERVAL_MS = 5_000
const VISIBILITY_EVENT = 'visibilitychange'
const FOCUS_EVENT = 'focus'
const STATE_VISIBLE: DocumentVisibilityState = 'visible'

/**
 * 用户回到页面（标签页重新可见或窗口重新获得焦点）时调用 onReturn，用来重取可能在别处变了的数据。
 * 页面不可见时不触发；两次触发之间至少隔 MIN_PAGE_RETURN_INTERVAL_MS。
 * @param onReturn 回到页面时执行的函数，调用方用 useCallback 固定引用
 * @param isEnabled 为 false 时不监听（如未登录、请求还在进行）；默认 true
 */
export function usePageReturn(onReturn: () => void, isEnabled = true): void {
  const lastRunAt = useRef(0)

  useEffect(() => {
    if (!isEnabled) {
      return undefined
    }
    const handle = () => {
      if (document.visibilityState !== STATE_VISIBLE) {
        return
      }
      const now = Date.now()
      if (now - lastRunAt.current < MIN_PAGE_RETURN_INTERVAL_MS) {
        return
      }
      lastRunAt.current = now
      onReturn()
    }
    document.addEventListener(VISIBILITY_EVENT, handle)
    window.addEventListener(FOCUS_EVENT, handle)
    return () => {
      document.removeEventListener(VISIBILITY_EVENT, handle)
      window.removeEventListener(FOCUS_EVENT, handle)
    }
  }, [onReturn, isEnabled])
}
