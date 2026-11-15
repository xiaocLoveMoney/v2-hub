import { api, type Announcement } from '../api/client'
import { Markdown } from '../components/Markdown'
import { Alert, Badge, Card, Empty, PageHeader, Pager } from '../components/ui'
import { formatTime } from '../format'
import { usePagedRemote } from '../hooks/use-remote'
import { useI18n } from '../i18n/i18n-context'

const PAGE_SIZE = 10

const loadAnnouncements = (page: number, signal: AbortSignal) => api.GET('/api/user/announcements', { params: { query: { page, pageSize: PAGE_SIZE } }, signal })

/** 公告列表（docs/03 §5.9）：只有已发布的，置顶在前、新的在前；正文按 Markdown 渲染。 */
export function AnnouncementsPage() {
  const { t, tag } = useI18n()
  const announcements = usePagedRemote<Announcement>(loadAnnouncements)
  const items = announcements.data?.items ?? []

  return (
    <>
      <PageHeader title={t('menu.announcements')} />
      {announcements.error !== null && <Alert tone="plain">{announcements.error.message}</Alert>}
      {items.map((a) => (
        <Card key={a.id} title={a.title} extra={a.isPinned ? <Badge tone="warning">{t('ann.pinned')}</Badge> : undefined}>
          <Markdown source={a.content} />
          <p className="hub-field-hint">{formatTime(a.publishedAt, tag)}</p>
        </Card>
      ))}
      {announcements.data !== null && items.length === 0 && (
        <Card>
          <Empty>{t('ann.empty')}</Empty>
        </Card>
      )}
      <Pager page={announcements.page} pageSize={PAGE_SIZE} total={announcements.data?.total ?? 0} onChange={announcements.setPage} />
    </>
  )
}
