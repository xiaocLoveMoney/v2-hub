import { useEffect, useRef } from 'react'

/** 主机任务未结束时的刷新间隔（docs/05 M11：每 2 秒）。 */
export const HOST_TASK_POLL_INTERVAL_MS = 2000
/** IP 质量检测未结束时的刷新间隔（docs/05：每 3 秒；一次检测要几十秒）。 */
export const IP_CHECK_POLL_INTERVAL_MS = 3000

/**
 * 条件为真时每隔固定毫秒调用一次回调；条件变假或组件卸载即清掉定时器。
 * 管理端的轮询只走这里（docs/05），用在两处：主机任务还没结束、IP 质量检测还没结束。
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

/** 能被定时刷新的远端数据；useRemote 的结果就是这个形状。 */
export interface Pollable {
  isLoading: boolean
  reload: () => void
}

/**
 * 条件为真时定时刷新若干份远端数据。某一份上一次的请求还没回来就跳过它这一轮：
 * reload 会取消旧请求，网络慢于轮询间隔时不跳过就会一直拿不到结果。
 * @param remotes 要刷新的数据（useRemote 的结果）
 * @param isActive 为 true 时才轮询
 * @param intervalMs 间隔毫秒，默认 HOST_TASK_POLL_INTERVAL_MS
 */
export function usePollRemote(remotes: readonly Pollable[], isActive: boolean, intervalMs: number = HOST_TASK_POLL_INTERVAL_MS): void {
  usePolling(
    () => {
      for (const remote of remotes) {
        if (!remote.isLoading) {
          remote.reload()
        }
      }
    },
    isActive,
    intervalMs,
  )
}
