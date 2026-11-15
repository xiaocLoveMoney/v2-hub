import { InputNumber, Tag, Typography, type TableProps } from 'antd'
import { useCallback, useState } from 'react'
import { api, type SubscribeLog } from '../../api/client'
import { PagedTable } from '../../components/PagedTable'
import { formatTime } from '../system/format'

/** 订阅格式的显示名；取值见 hub-api.yaml 的 SubscriptionFormat。 */
const FORMAT_LABELS: Record<SubscribeLog['format'], string> = {
  base64: '通用（Base64）',
  mihomo: 'Mihomo / Clash',
  singbox: 'sing-box',
  xray: 'Xray JSON',
}

/** 订阅拉取记录（user:read），按时间倒序，可按用户 ID 过滤（docs/03 §5.4 记录）。 */
export function SubscribeLogsPage() {
  const [userId, setUserId] = useState<number | undefined>(undefined)
  const loadLogs = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) => api.GET('/api/admin/subscribe-logs', { params: { query: { page, pageSize, userId } }, signal }),
    [userId],
  )

  const columns: TableProps<SubscribeLog>['columns'] = [
    { title: '时间', dataIndex: 'createdAt', width: 180, render: (v: string) => formatTime(v) },
    { title: '用户', key: 'user', render: (_: unknown, l: SubscribeLog) => `${l.username} #${l.userId}` },
    { title: '格式', dataIndex: 'format', render: (f: SubscribeLog['format']) => <Tag color="blue">{FORMAT_LABELS[f] ?? f}</Tag> },
    { title: 'IP', dataIndex: 'clientIp', render: (ip: string) => ip || '—' },
    {
      title: '客户端标识',
      dataIndex: 'userAgent',
      render: (ua: string) => (
        <Typography.Text ellipsis={{ tooltip: ua }} className="audit-detail">
          {ua || '—'}
        </Typography.Text>
      ),
    },
  ]

  return (
    <>
      <Typography.Title level={3}>订阅记录</Typography.Title>
      <PagedTable<SubscribeLog>
        load={loadLogs}
        columns={columns}
        filters={
          <InputNumber
            min={1}
            precision={0}
            placeholder="按用户 ID 过滤"
            aria-label="按用户 ID 过滤"
            className="filter-select"
            value={userId}
            onChange={(v) => setUserId(v === null ? undefined : v)}
          />
        }
      />
    </>
  )
}
