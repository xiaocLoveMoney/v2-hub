import { Select, Tag, Typography, type TableProps } from 'antd'
import { useCallback, useState } from 'react'
import { api, type MailOutboxItem, type MailStatus } from '../../api/client'
import { PagedTable } from '../../components/PagedTable'
import { formatTime } from './format'

/** 发件状态的显示名与颜色；取值见 hub-api.yaml 的 MailStatus。 */
const STATUS_META: Record<MailStatus, { label: string; color: string }> = {
  pending: { label: '待发送', color: 'blue' },
  sent: { label: '已发送', color: 'green' },
  failed: { label: '失败', color: 'red' },
}
const STATUS_OPTIONS = (Object.keys(STATUS_META) as MailStatus[]).map((value) => ({ value, label: STATUS_META[value].label }))

/** 发件记录（setting:read）：只有收件人（已打码）、主题与状态，不含正文（docs/03 §5.7）。 */
export function MailOutboxPage() {
  const [status, setStatus] = useState<MailStatus | undefined>(undefined)
  const loadMails = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) => api.GET('/api/admin/mail-outbox', { params: { query: { page, pageSize, status } }, signal }),
    [status],
  )

  const columns: TableProps<MailOutboxItem>['columns'] = [
    { title: '入队时间', dataIndex: 'createdAt', width: 180, render: (v: string) => formatTime(v) },
    { title: '收件人', dataIndex: 'recipient' },
    { title: '主题', dataIndex: 'subject' },
    { title: '状态', dataIndex: 'status', render: (s: MailStatus) => <Tag color={STATUS_META[s]?.color}>{STATUS_META[s]?.label ?? s}</Tag> },
    { title: '尝试次数', dataIndex: 'attempts' },
    {
      title: '最近错误',
      dataIndex: 'lastError',
      render: (text: string) => (
        <Typography.Text ellipsis={{ tooltip: text }} className="audit-detail">
          {text || '—'}
        </Typography.Text>
      ),
    },
    { title: '发出时间', dataIndex: 'sentAt', render: (v: string | null) => formatTime(v) },
  ]

  return (
    <>
      <Typography.Title level={3}>发件记录</Typography.Title>
      <PagedTable<MailOutboxItem>
        load={loadMails}
        columns={columns}
        filters={
          <Select
            allowClear
            placeholder="按状态过滤"
            aria-label="按状态过滤"
            className="filter-select"
            options={STATUS_OPTIONS}
            value={status}
            onChange={(v: MailStatus | undefined) => setStatus(v)}
          />
        }
      />
    </>
  )
}
