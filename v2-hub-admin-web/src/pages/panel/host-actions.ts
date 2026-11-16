import { api, csrfHeader, toApiError, type ApiError, type HostAction, type HostTask } from '../../api/client'

/** 发起动作的结果：成功时 task 是排队中的任务，失败时 error 非空。 */
export interface HostActionResult {
  task: HostTask | null
  error: ApiError | null
}

/**
 * 对主机发起一个动作，排成一条任务（host:write；docs/03 §5.22 任务、运维动作）。
 * 该主机已有未结束的任务、或动作与主机是否已关联服务器不符时后端返回 409。
 * @param hostId 主机 ID
 * @param action 动作
 * @returns 排队中的任务或错误，不抛异常
 */
export async function startHostAction(hostId: number, action: HostAction): Promise<HostActionResult> {
  try {
    const { data, error } = await api.POST('/api/admin/server-hosts/{id}/actions', {
      params: { ...csrfHeader(), path: { id: hostId } },
      body: { action },
    })
    if (error !== undefined || data?.data === undefined) {
      return { task: null, error: toApiError(error) }
    }
    return { task: data.data, error: null }
  } catch (err) {
    reportError(err)
    return { task: null, error: toApiError(err) }
  }
}

/** 抽屉里正在看的主机任务：哪台主机、先选中哪条（null = 最新一条）。 */
export interface OpenedHostTasks {
  host: { id: number; name: string }
  taskId: number | null
}
