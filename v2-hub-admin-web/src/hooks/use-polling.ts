import { useEffect, useRef } from 'react'

/** 主机任务未结束时的刷新间隔（docs/05 M11：每 2 秒）。 */
export const HOST_TASK_POLL_INTERVAL_MS = 2000

/**
 * 条件为真时每隔固定毫秒调用一次回调；条件变假或组件卸载即清掉定时器。
 * 管理端唯一的轮询（docs/05 M11），只给“主机任务还没结束”这一种场景用。
 * @param callback 每次到点调用；总是调用最近一次渲染传入的那个，不必用 useCallback 固定
 * @param isActive 为 true 时才轮询
 * @param intervalMs 间隔毫秒，默认 HOST_TASK_POLL_INTERVAL_MS
 */
export function usePolling(callback: () => void, isActive: boolean, intervalMs: number = HOST_TASK_POLL_INTERVAL_MS): void {
  const latest = useRef(callback)
  useEffect(() => {
    latest.current = callback
  })
  useEffect(() => {
    if (!isActive) {
      return
    }
    const timer = setInterval(() => latest.current(), intervalMs)
    return () => clearInterval(timer)
  }, [isActive, intervalMs])
}
