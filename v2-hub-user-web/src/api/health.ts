/** 后端接口路径常量；业务接口随 hub-api.yaml 生成的 client 落到 src/api/generated/。 */
export const API_PATHS = {
  HEALTHZ: '/healthz',
} as const

export const HEALTH_STATUS_OK = 'ok'

/** /healthz 响应体，对应 hub-api.yaml components.schemas.Health。 */
export interface HealthResponse {
  status: string
}

/**
 * 探测 hub-server 是否存活。
 * @param signal 组件卸载时用于取消请求
 * @returns 后端存活返回 true，状态码非 2xx 或响应不符返回 false
 * @throws 网络错误或请求被取消时抛出（由调用方处理）
 */
export async function fetchHealth(signal?: AbortSignal): Promise<boolean> {
  const resp = await fetch(API_PATHS.HEALTHZ, { signal })
  if (!resp.ok) {
    return false
  }
  const body = (await resp.json()) as HealthResponse
  return body.status === HEALTH_STATUS_OK
}
