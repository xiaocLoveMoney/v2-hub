import { Tag, type TableProps } from 'antd'
import type { AgentAllocation, AgentAllocationKind, AgentPlan, AgentSummary, Plan } from '../../api/client'
import { formatTime } from '../system/format'
import { formatBytes } from '../users/quota'

// 代理界面与超管侧代理管理共用的格式化（docs/03 §5.23，Ruling-103）；流量池不足的错误提示由 api/client.ts 的 toApiError 统一换

/** 开通记录类型的显示名；取值见 hub-api.yaml 的 AgentAllocationKind。 */
const ALLOCATION_KIND_LABELS: Record<AgentAllocationKind, string> = { create: '新建用户', assign: '分配 / 续期' }

/** 流量池还剩多少（字节）；超管调低总量后不会是负数，这里兜底按 0。 */
export function poolRemainingOf(summary: Pick<AgentSummary, 'trafficPoolTotal' | 'trafficPoolUsed'>): number {
  return Math.max(0, summary.trafficPoolTotal - summary.trafficPoolUsed)
}

/** 套餐的额度说明：流量 · 时长（代理的套餐下拉与超管的白名单下拉共用）。 */
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
