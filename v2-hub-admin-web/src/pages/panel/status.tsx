import { Tag, Tooltip } from 'antd'
import { PLAIN_HTTP_TAG, PLAIN_HTTP_WARNING, isPlainHttpUrl } from './host-labels'

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

/** 面板地址是明文 HTTP 时的警告标签，悬停说明风险与处理办法（docs/03 §5.22 接管）；不是明文 HTTP 不渲染。 */
export function PlainHttpTag({ baseUrl }: { baseUrl: string }) {
  if (!isPlainHttpUrl(baseUrl)) {
    return null
  }
  return (
    <Tooltip title={PLAIN_HTTP_WARNING}>
      <Tag color="orange">{PLAIN_HTTP_TAG}</Tag>
    </Tooltip>
  )
}
