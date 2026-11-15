import { api, type SubscribeLog } from '../api/client'
import { Alert, Badge, Card, DataTable, PageHeader, Pager } from '../components/ui'
import { formatTime } from '../format'
import { usePagedRemote } from '../hooks/use-remote'
import { useI18n, type MessageKey } from '../i18n/i18n-context'
import { ConnectionPasswordCard } from './ConnectionPasswordCard'

const PAGE_SIZE = 20

const FORMAT_LABELS: Record<SubscribeLog['format'], MessageKey> = {
  base64: 'subLogs.generic',
  mihomo: 'subLogs.formatMihomo',
  singbox: 'subLogs.formatSingbox',
  xray: 'subLogs.formatXray',
}

const loadLogs = (page: number, signal: AbortSignal) => api.GET('/api/user/subscribe-logs', { params: { query: { page, pageSize: PAGE_SIZE } }, signal })

/** 订阅记录：本人的订阅地址被拉取的时间、格式、IP 与客户端标识（docs/03 §5.4 记录）。出现陌生 IP 时应重置订阅。 */
export function SubscribeLogsPage() {
  const { t, tag } = useI18n()
  const logs = usePagedRemote<SubscribeLog>(loadLogs)

  return (
    <>
      <PageHeader title={t('menu.subscribeLogs')} />
      <ConnectionPasswordCard />
      <Card>
        {logs.error !== null && <Alert tone="plain">{logs.error.message}</Alert>}
        <p className="hub-field-hint" style={{ marginTop: 0, marginBottom: 14 }}>
          {t('subLogs.warning')}
        </p>
        <DataTable headers={[t('subLogs.colTime'), t('subLogs.colFormat'), 'IP', t('subLogs.colClient')]} isEmpty={logs.data !== null && logs.data.items.length === 0} emptyText={t('subLogs.empty')}>
          {(logs.data?.items ?? []).map((l) => (
            <tr key={l.id}>
              <td>{formatTime(l.createdAt, tag)}</td>
              <td>
                <Badge tone="light">{l.format in FORMAT_LABELS ? t(FORMAT_LABELS[l.format]) : l.format}</Badge>
              </td>
              <td>{l.clientIp || t('common.dash')}</td>
              <td>{l.userAgent || t('common.dash')}</td>
            </tr>
          ))}
        </DataTable>
        <Pager page={logs.page} pageSize={PAGE_SIZE} total={logs.data?.total ?? 0} onChange={logs.setPage} />
      </Card>
    </>
  )
}
