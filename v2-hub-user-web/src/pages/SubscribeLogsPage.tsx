import { useCallback, useState } from 'react'
import { api, type PageData, type SubscribeLog } from '../api/client'
import { Alert, Badge, Button, Card, Empty, PageHeader } from '../components/ui'
import { formatTime } from '../format'
import { useRemote } from '../hooks/use-remote'

const PAGE_SIZE = 20

const FORMAT_LABELS: Record<SubscribeLog['format'], string> = {
  base64: '通用',
  mihomo: 'Clash / Mihomo',
  singbox: 'sing-box',
  xray: 'Xray JSON',
}

/** 订阅记录：本人的订阅地址被拉取的时间、格式、IP 与客户端标识（docs/03 §5.4 记录）。出现陌生 IP 时应重置订阅。 */
export function SubscribeLogsPage() {
  const [page, setPage] = useState(1)
  const load = useCallback((signal: AbortSignal) => api.GET('/api/user/subscribe-logs', { params: { query: { page, pageSize: PAGE_SIZE } }, signal }), [page])
  const logs = useRemote<PageData<SubscribeLog>>(load)
  const total = logs.data?.total ?? 0
  const pageCount = Math.max(Math.ceil(total / PAGE_SIZE), 1)

  return (
    <>
      <PageHeader title="订阅记录" />
      <Card>
        {logs.error !== null && <Alert tone="plain">{logs.error.message}</Alert>}
        <p className="hub-field-hint" style={{ marginTop: 0, marginBottom: 14 }}>
          如果出现不认识的 IP 或客户端，说明订阅地址可能已泄露，请到仪表盘重置订阅。
        </p>
        <div className="hub-table-wrap">
          <table className="hub-table">
            <thead>
              <tr>
                <th>时间</th>
                <th>格式</th>
                <th>IP</th>
                <th>客户端标识</th>
              </tr>
            </thead>
            <tbody>
              {(logs.data?.items ?? []).map((l) => (
                <tr key={l.id}>
                  <td>{formatTime(l.createdAt)}</td>
                  <td>
                    <Badge tone="light">{FORMAT_LABELS[l.format] ?? l.format}</Badge>
                  </td>
                  <td>{l.clientIp || '—'}</td>
                  <td>{l.userAgent || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {logs.data !== null && logs.data.items.length === 0 && <Empty>还没有订阅记录</Empty>}
        {total > PAGE_SIZE && (
          <div className="hub-pagination">
            <Button variant="outline" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>
              上一页
            </Button>
            <span>
              {page} / {pageCount}
            </span>
            <Button variant="outline" disabled={page >= pageCount} onClick={() => setPage((p) => p + 1)}>
              下一页
            </Button>
          </div>
        )}
      </Card>
    </>
  )
}
