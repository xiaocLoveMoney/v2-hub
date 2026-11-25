import { Tag, type TableProps } from 'antd'
import {
  CODE_AGENT_POOL_INSUFFICIENT,
  CODE_AGENT_USER_LIMIT,
  CODE_RATE_LIMITED,
  type AgentAllocation,
  type AgentAllocationKind,
  type AgentPlan,
  type AgentSummary,
  type Plan,
} from '../../api/client'
import { formatTime } from '../system/format'
import { formatBytes } from '../users/quota'

// 代理界面与超管侧代理管理共用的格式化与错误提示（docs/03 §5.23，Ruling-103）

/** 开通记录类型的显示名；取值见 hub-api.yaml 的 AgentAllocationKind。 */
const ALLOCATION_KIND_LABELS: Record<AgentAllocationKind, string> = { create: '新建用户', assign: '分配 / 续期' }

const USER_LIMIT_TEXT = '名下用户数已达上限，不能再新建；请联系平台调整上限'
const RATE_LIMITED_TEXT = '操作过于频繁，请稍后再试'

/** 流量池还剩多少（字节）；超管调低总量后不会是负数，这里兜底按 0。 */
export function poolRemainingOf(summary: Pick<AgentSummary, 'trafficPoolTotal' | 'trafficPoolUsed'>): number {
  return Math.max(0, summary.trafficPoolTotal - summary.trafficPoolUsed)
}

/** 流量池不够时的提示文字。 */
export function poolInsufficientText(cost: number, remaining: number): string {
  return `本次需要 ${formatBytes(cost)}，流量池只剩 ${formatBytes(remaining)}，请联系平台补充`
}

/** 从错误信封的 data 里取一个数值字段；取不到返回 null。 */
function numberField(data: unknown, key: string): number | null {
  if (typeof data !== 'object' || data === null || !(key in data)) {
    return null
  }
  const value = (data as Record<string, unknown>)[key]
  return typeof value === 'number' ? value : null
}

/** 把代理接口的错误信封换成给人看的提示；不认识的错误码原样交还。 */
function describeAgentError(error: unknown): unknown {
  if (typeof error !== 'object' || error === null || !('code' in error)) {
    return error
  }
  const envelope = error as { code: unknown; message?: unknown; data?: unknown }
  switch (envelope.code) {
    case CODE_AGENT_POOL_INSUFFICIENT: {
      const cost = numberField(envelope.data, 'poolCost')
      const remaining = numberField(envelope.data, 'poolRemaining')
      const isDetailed = cost !== null && remaining !== null
      return { code: envelope.code, message: isDetailed ? poolInsufficientText(cost, remaining) : '流量池不够本次开通，请联系平台补充' }
    }
    case CODE_AGENT_USER_LIMIT:
      return { code: envelope.code, message: USER_LIMIT_TEXT }
    case CODE_RATE_LIMITED:
      return { code: envelope.code, message: RATE_LIMITED_TEXT }
    default:
      return error
  }
}

/**
 * 包一层代理的写操作：流量池不足、用户数上限、频控三种错误换成具体提示，其余照旧（交给 useSubmit / useRowAction 显示）。
 * @param action 原写操作
 * @returns 同形状的写操作
 */
export function withAgentErrors(action: () => Promise<{ error?: unknown }>): () => Promise<{ error?: unknown }> {
  return async () => {
    const res = await action()
    return res.error === undefined ? res : { ...res, error: describeAgentError(res.error) }
  }
}

/** 套餐的额度说明：流量 / 时长 / 重置。 */
export function describeAgentPlan(plan: Pick<AgentPlan, 'trafficLimit' | 'durationDays'>): string {
  return `${formatBytes(plan.trafficLimit)} · ${plan.durationDays} 天`
}

/**
 * 开通记录表的列（代理的配额概览与超管的开通记录共用）。
 * @returns 新数组
 */
export function allocationColumns(): TableProps<AgentAllocation>['columns'] {
  return [
    { title: '时间', dataIndex: 'createdAt', render: (v: string) => formatTime(v) },
    {
      title: '用户',
      key: 'user',
      render: (_: unknown, a: AgentAllocation) => (a.userId === null || a.userId === undefined ? `${a.username}（已删除）` : `${a.username} #${a.userId}`),
    },
    { title: '套餐', key: 'plan', render: (_: unknown, a: AgentAllocation) => (a.planId === null || a.planId === undefined ? `${a.planName}（已删除）` : a.planName) },
    { title: '类型', dataIndex: 'kind', render: (k: AgentAllocationKind) => <Tag>{ALLOCATION_KIND_LABELS[k] ?? k}</Tag> },
    { title: '时长', dataIndex: 'durationDays', render: (v: number) => `${v} 天` },
    { title: '扣减流量', dataIndex: 'trafficBytes', render: (v: number) => formatBytes(v) },
  ]
}

/** 能进白名单的套餐：流量与时长都有限（Ruling-103）。 */
export function isWhitelistable(plan: Pick<Plan, 'trafficLimit' | 'durationDays'>): boolean {
  return plan.trafficLimit > 0 && plan.durationDays > 0
}
