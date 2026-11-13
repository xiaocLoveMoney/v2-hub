import { Alert, App, Button, Descriptions, Space, Tag, Typography } from 'antd'
import { useCallback, useState } from 'react'
import { api, csrfHeader, type ChainApply } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { mutate, useRemote } from '../../hooks/use-remote'
import { PERMISSIONS } from '../../menu'
import { formatTime } from '../system/format'
import { CHAIN_APPLY_STATUS_META } from './chain-labels'

/**
 * 这台服务器作为服务端链式入口的下发状态（docs/03 §5.19，server:read）；失败停止自动重试后可点“重试”（server:write）。
 * 面板不支持改内核配置时只给一行说明。
 */
export function ChainApplyCard({ serverId }: { serverId: number }) {
  const { can } = useSession()
  const { message } = App.useApp()
  const [isBusy, setIsBusy] = useState(false)
  const load = useCallback(
    (signal: AbortSignal) => api.GET('/api/admin/servers/{id}/chain-apply', { params: { path: { id: serverId } }, signal }),
    [serverId],
  )
  const state = useRemote<ChainApply>(load)
  const a = state.data

  const retry = async () => {
    setIsBusy(true)
    const err = await mutate(() => api.POST('/api/admin/servers/{id}/chain-apply/retry', { params: { ...csrfHeader(), path: { id: serverId } } }))
    setIsBusy(false)
    if (err !== null) {
      void message.error(err.message)
      return
    }
    void message.success('已触发重新下发')
    state.reload()
  }

  if (state.error !== null) {
    return <Alert type="error" title={state.error.message} showIcon className="page-alert" />
  }
  if (a === null) {
    return null
  }
  if (!a.isSupported) {
    return <Typography.Paragraph type="secondary">这台服务器的面板不支持改内核配置，不能作服务端链式的入口。</Typography.Paragraph>
  }
  const meta = CHAIN_APPLY_STATUS_META[a.status]
  const canRetry = can(PERMISSIONS.SERVER_WRITE) && a.status === 'failed'
  return (
    <>
      <Space className="page-header">
        <Typography.Title level={4}>服务端链式下发</Typography.Title>
        {canRetry && (
          <Button onClick={() => void retry()} loading={isBusy}>
            重试
          </Button>
        )}
      </Space>
      {a.isRetryStopped && <Alert type="warning" showIcon className="page-alert" title="同一配置连续失败 3 次，已停止自动重试；规则变化或点“重试”后再下发" />}
      <Descriptions size="small" column={{ xs: 1, md: 3 }} className="page-alert">
        <Descriptions.Item label="状态">
          <Tag color={meta.color}>{meta.label}</Tag>
        </Descriptions.Item>
        <Descriptions.Item label="生效 / 期望规则">
          {a.appliedRuleCount} / {a.ruleCount}
        </Descriptions.Item>
        <Descriptions.Item label="最近生效">{formatTime(a.appliedAt)}</Descriptions.Item>
        <Descriptions.Item label="最近尝试">{formatTime(a.lastAttemptAt)}</Descriptions.Item>
        <Descriptions.Item label="失败次数">{a.failCount}</Descriptions.Item>
        <Descriptions.Item label="下次重试">{formatTime(a.nextAttemptAt)}</Descriptions.Item>
        {a.lastError !== '' && (
          <Descriptions.Item label="最近错误" span={3}>
            <Typography.Text type="danger">{a.lastError}</Typography.Text>
          </Descriptions.Item>
        )}
      </Descriptions>
      <Typography.Paragraph type="secondary">
        平台只增删 Xray 模板里 <Typography.Text code>hub-</Typography.Text> 开头的出站与路由；请不要在面板上手改这些条目，改了会在下一轮被替换。
      </Typography.Paragraph>
    </>
  )
}
