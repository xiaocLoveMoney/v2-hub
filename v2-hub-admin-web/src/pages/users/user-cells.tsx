import { Progress, Tooltip } from 'antd'
import { describeExpiry, formatBytes } from './quota'

const PERCENT_FULL = 100

/** 用量单元格需要的字段：员工侧的用户与代理侧的名下用户都有。 */
interface UsageFields {
  trafficUsed: number
  /** 0 = 不限。 */
  trafficLimit: number
}

/** 到期单元格需要的字段。 */
interface ExpiryFields {
  expiresAt?: string | null
  pendingDays: number
}

/** 已用 / 额度：有额度时画进度条（悬停看具体数值），不限时只写已用。 */
export function UsageCell({ u }: { u: UsageFields }) {
  if (u.trafficLimit === 0) {
    return <span>{formatBytes(u.trafficUsed)} / 不限</span>
  }
  const percent = Math.min(PERCENT_FULL, Math.round((u.trafficUsed / u.trafficLimit) * PERCENT_FULL))
  return (
    <Tooltip title={`${formatBytes(u.trafficUsed)} / ${formatBytes(u.trafficLimit)}`}>
      <Progress percent={percent} size="small" status={percent >= PERCENT_FULL ? 'exception' : 'normal'} className="usage-bar" />
    </Tooltip>
  )
}

/** 到期单元格。 */
export function ExpiryCell({ u }: { u: ExpiryFields }) {
  return <span>{describeExpiry(u)}</span>
}
