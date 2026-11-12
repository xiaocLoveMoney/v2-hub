import { Tag, Tooltip } from 'antd'

/** 服务器状态展示，取值与 docs/03 §2 一致。 */
const STATUS_VIEW: Record<string, { color: string; label: string }> = {
  online: { color: 'green', label: '在线' },
  offline: { color: 'red', label: '离线' },
  auth_failed: { color: 'orange', label: 'token 失效' },
  unknown: { color: 'default', label: '未探测' },
}

interface ServerStatusTagProps {
  status: string
  lastError: string
  isDisabled: boolean
}

/** 服务器状态标签；异常时悬停显示脱敏后的原因。 */
export function ServerStatusTag({ status, lastError, isDisabled }: ServerStatusTagProps) {
  if (isDisabled) {
    return <Tag>已停用</Tag>
  }
  const view = STATUS_VIEW[status] ?? STATUS_VIEW.unknown
  const tag = <Tag color={view.color}>{view.label}</Tag>
  return lastError === '' ? tag : <Tooltip title={lastError}>{tag}</Tooltip>
}
