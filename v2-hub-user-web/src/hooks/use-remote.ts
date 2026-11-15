import { useCallback, useEffect, useState } from 'react'
import { toApiError, type ApiError, type PageData } from '../api/client'

/** openapi-fetch 调用结果的最小形状。 */
interface FetchResult<T> {
  data?: { data?: T }
  error?: unknown
}

interface RemoteState<T> {
  data: T | null
  error: ApiError | null
  isLoading: boolean
  /** 重新拉取（增删改后调用）。 */
  reload: () => void
}

/**
 * 拉取一份远端数据：load 变化或调用 reload 时重新请求，卸载 / 重新请求时取消旧请求，避免过期响应覆盖新数据。
 * @param load 发起请求的函数，调用方用 useCallback 固定并列出依赖；必须把 signal 传给 openapi-fetch
 */
export function useRemote<T>(load: (signal: AbortSignal) => Promise<FetchResult<T>>): RemoteState<T> {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<ApiError | null>(null)
  const [isLoading, setIsLoading] = useState(true)
  const [version, setVersion] = useState(0)

  useEffect(() => {
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
  }, [load, version])

  const reload = useCallback(() => setVersion((v) => v + 1), [])
  return { data, error, isLoading, reload }
}

/** 分页列表的页码从 1 开始。 */
const FIRST_PAGE = 1

interface PagedRemoteState<T> extends RemoteState<PageData<T>> {
  /** 当前页码，从 1 开始。 */
  page: number
  /** 翻页：页码变化后重新请求。 */
  setPage: (page: number) => void
}

/**
 * 拉取一份分页列表：在 useRemote 之上管理页码，翻页、loadPage 变化或 reload 时重新请求。
 * @param loadPage 按页码发起请求的函数，调用方用模块级函数或 useCallback 固定引用；必须把 signal 传给 openapi-fetch
 */
export function usePagedRemote<T>(loadPage: (page: number, signal: AbortSignal) => Promise<FetchResult<PageData<T>>>): PagedRemoteState<T> {
  const [page, setPage] = useState(FIRST_PAGE)
  const load = useCallback((signal: AbortSignal) => loadPage(page, signal), [loadPage, page])
  const remote = useRemote<PageData<T>>(load)
  return { ...remote, page, setPage }
}

/** request 的结果：成功时 data 是响应信封里的 data，失败时 error 是转好的 ApiError，两者恰有一个非空。 */
export type RequestResult<T> = { data: T; error: null } | { data: null; error: ApiError }

/** 跑一次请求并兜住异常：网络错误等抛出的异常先上报，再转成 ApiError。 */
async function settle<R>(run: () => Promise<R>): Promise<{ res: R; error: null } | { res: null; error: ApiError }> {
  try {
    return { res: await run(), error: null }
  } catch (err) {
    reportError(err)
    return { res: null, error: toApiError(err) }
  }
}

/**
 * 执行一次要读返回值的请求（读凭据、提交表单等），统一把失败转成 ApiError。
 * 响应带 error 或没有 data 都算失败。
 * @param run 发起请求的函数
 * @returns 成功时 { data, error: null }，失败时 { data: null, error }
 */
export async function request<T>(run: () => Promise<FetchResult<T>>): Promise<RequestResult<T>> {
  const settled = await settle(run)
  if (settled.error !== null) {
    return { data: null, error: settled.error }
  }
  const { res } = settled
  if (res.error !== undefined || res.data?.data === undefined) {
    return { data: null, error: toApiError(res.error) }
  }
  return { data: res.data.data, error: null }
}

/** 执行一次写操作，统一把失败转成 ApiError；成功返回 null（不要求响应带 data）。 */
export async function mutate(run: () => Promise<{ error?: unknown }>): Promise<ApiError | null> {
  const settled = await settle(run)
  if (settled.error !== null) {
    return settled.error
  }
  return settled.res.error === undefined ? null : toApiError(settled.res.error)
}
