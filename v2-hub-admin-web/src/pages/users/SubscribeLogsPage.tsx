import { Alert, InputNumber, Space, Table, Tag, Typography, type TableProps } from 'antd'
import { useCallback, useState } from 'react'
import { api, type PageData, type SubscribeLog } from '../../api/client'
import { useRemote } from '../../hooks/use-remote'
import { TABLE_SCROLL, formatTime } from '../system/format'

const DEFAULT_PAGE_SIZE = 20

/** 订阅格式的显示名；取值见 hub-api.yaml 的 SubscriptionFormat。 */
const FORMAT_LABELS: Record<SubscribeLog['format'], string> = {
  base64: '通用（Base64）',
  mihomo: 'Mihomo / Clash',
  singbox: 'sing-box',
  xray: 'Xray JSON',
}

/** 订阅拉取记录（user:read），按时间倒序，可按用户 ID 过滤（docs/03 §5.4 记录）。 */
export function SubscribeLogsPage() {
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(DEFAULT_PAGE_SIZE)
  const [userId, setUserId] = useState<number | undefined>(undefined)
  const loadLogs = useCallback(
    (signal: AbortSignal) => api.GET('/api/admin/subscribe-logs', { params: { query: { page, pageSize, userId } }, signal }),
    [page, pageSize, userId],
  )
  const logs = useRemote<PageData<SubscribeLog>>(loadLogs)

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
      <Space wrap className="page-filters">
        <InputNumber
          min={1}
          precision={0}
          placeholder="按用户 ID 过滤"
          aria-label="按用户 ID 过滤"
          className="filter-select"
          value={userId}
          onChange={(v) => {
            setPage(1)
            setUserId(v === null ? undefined : v)
          }}
        />
      </Space>
      {logs.error !== null && <Alert type="error" title={logs.error.message} showIcon className="page-alert" />}
      <Table<SubscribeLog>
        rowKey="id"
        scroll={TABLE_SCROLL}
        loading={logs.isLoading}
        columns={columns}
        dataSource={logs.data?.items ?? []}
        pagination={{
          current: page,
          pageSize,
          total: logs.data?.total ?? 0,
          onChange: (p, size) => {
            setPage(p)
            setPageSize(size)
          },
        }}
      />
    </>
  )
}
