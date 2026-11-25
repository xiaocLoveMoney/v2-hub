import { Alert } from 'antd'
import type { AgentPlan, AgentSummary } from '../../api/client'
import { formatBytes } from '../users/quota'
import { poolInsufficientText, poolRemainingOf } from './agent-format'

interface PoolDeductionProps {
  /** 选中的套餐；没选时不显示。 */
  plan: AgentPlan | undefined
  /** 配额；还没取到时只显示扣减量。 */
  summary: AgentSummary | null
}

/** 开通前的扣减提示：本次从流量池扣多少、池里还剩多少；不够时变成警告（后端仍是最终裁决）。 */
export function PoolDeduction({ plan, summary }: PoolDeductionProps) {
  if (plan === undefined) {
    return null
  }
  const cost = `本次从流量池扣减 ${formatBytes(plan.poolCost)}`
  if (summary === null) {
    return <Alert type="info" showIcon className="page-alert" title={cost} />
  }
  const remaining = poolRemainingOf(summary)
  if (plan.poolCost > remaining) {
    return <Alert type="warning" showIcon className="page-alert" title={poolInsufficientText(plan.poolCost, remaining)} />
  }
  return <Alert type="info" showIcon className="page-alert" title={`${cost}，流量池剩余 ${formatBytes(remaining)}`} description="删除用户、调低额度、套餐到期都不退回" />
}
