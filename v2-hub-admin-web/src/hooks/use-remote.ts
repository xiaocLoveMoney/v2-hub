import { useCallback, useEffect, useState } from 'react'
import { toApiError, type ApiError } from '../api/client'

/** openapi-fetch 调用结果的最小形状。 */
interface FetchResult<T> {
  data?: { data?: T }
  error?: unknown
}

/** useRemote 的结果：数据、错误、是否在加载与重新拉取。 */
export interface RemoteState<T> {
  data: T | null
  error: ApiError | null
  isLoading: boolean
  /** 重新拉取（增删改后调用）。 */
  reload: () => void
}

/**
 * 拉取一份远端数据：load 变化或调用 reload 时重新请求，卸载 / 重新请求时取消旧请求，避免过期响应覆盖新数据。
 * @param load 发起请求的函数，调用方用 useCallback 固定并列出依赖；必须把 signal 传给 openapi-fetch
 * @param isEnabled 为 false 时不发请求（如没有读权限），返回空数据、无错误、不在加载；默认 true
 */
export function useRemote<T>(load: (signal: AbortSignal) => Promise<FetchResult<T>>, isEnabled = true): RemoteState<T> {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<ApiError | null>(null)
  const [isLoading, setIsLoading] = useState(true)
  const [version, setVersion] = useState(0)

  useEffect(() => {
    if (!isEnabled) {
      return
    }
    const controller = new AbortController()
    const run = async () => {
      setIsLoading(true)
      try {
        const res = await load(controller.signal)
        if (controller.signal.aborted) {
          return
        }
        if (res.error !== undefined || res.data?.data === undefined) {
          setError(toApiError(res.error))
          return
        }
        setError(null)
        setData(res.data.data)
      } catch (err) {
        if (controller.signal.aborted) {
          return
        }
        reportError(err)
        setError(toApiError(err))
      } finally {
        if (!controller.signal.aborted) {
          setIsLoading(false)
        }
      }
    }
    void run()
    return () => controller.abort()
  }, [load, version, isEnabled])

  const reload = useCallback(() => setVersion((v) => v + 1), [])
  if (!isEnabled) {
    return { data: null, error: null, isLoading: false, reload }
  }
  return { data, error, isLoading, reload }
}

/** 执行一次写操作，统一把失败转成 ApiError；成功返回 null。 */
export async function mutate(run: () => Promise<{ error?: unknown }>): Promise<ApiError | null> {
  try {
    const res = await run()
    return res.error === undefined ? null : toApiError(res.error)
  } catch (err) {
    reportError(err)
    return toApiError(err)
  }
}
