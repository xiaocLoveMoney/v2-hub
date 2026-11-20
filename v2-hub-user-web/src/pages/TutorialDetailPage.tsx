import { Link, useParams } from 'react-router'
import type { Tutorial } from '../api/client'
import { HTTPS_PREFIX } from '../app-config'
import { Markdown } from '../components/Markdown'
import { Alert, Card, Loading, PageHeader } from '../components/ui'
import { useRemote } from '../hooks/use-remote'
import { useI18n } from '../i18n/i18n-context'
import { ROUTES } from '../menu'
import { NotFoundPage } from './NotFoundPage'
import { loadTutorials, tutorialListPath } from './tutorials'

const TUTORIAL_ID_PATTERN = /^[1-9]\d{0,17}$/

/** 教程详情页（Ruling-90）：下载链接、正文与去仪表盘导入订阅的入口。复用列表接口，未发布或不存在的显示 404。 */
export function TutorialDetailPage() {
  const { id = '' } = useParams()
  if (!TUTORIAL_ID_PATTERN.test(id)) {
    return <NotFoundPage />
  }
  return <TutorialDetail tutorialId={Number(id)} />
}

function TutorialDetail({ tutorialId }: { tutorialId: number }) {
  const { t } = useI18n()
  const tutorials = useRemote<Tutorial[]>(loadTutorials)

  if (tutorials.data === null) {
    return (
      <>
        <PageHeader title={t('menu.tutorials')} extra={<Link to={ROUTES.TUTORIALS}>{t('tutorials.backToList')}</Link>} />
        {tutorials.error === null ? <Loading isInline /> : <Alert tone="plain">{tutorials.error.message}</Alert>}
      </>
    )
  }
  const tutorial = tutorials.data.find((item) => item.id === tutorialId)
  if (tutorial === undefined) {
    return <NotFoundPage />
  }
  // 后端已保证链接是 https；前端再判一次，不合规的不渲染成可点的链接
  const links = tutorial.links.filter((l) => l.url.startsWith(HTTPS_PREFIX))
  return (
    <>
      <PageHeader title={tutorial.clientName} extra={<Link to={tutorialListPath(tutorial.platform)}>{t('tutorials.backToList')}</Link>} />
      <Card>
        {tutorial.summary !== '' && <p className="hub-card-text">{tutorial.summary}</p>}
        {links.length > 0 && (
          <div className="hub-actions hub-row-gap">
            {links.map((l) => (
              <a key={`${l.label}-${l.url}`} className="hub-btn hub-btn-primary" href={l.url} target="_blank" rel="noopener noreferrer">
                {l.label}
              </a>
            ))}
          </div>
        )}
        {tutorial.content !== '' && <Markdown source={tutorial.content} />}
        <p className="hub-field-hint">
          {t('tutorials.importBefore')}
          <Link to={ROUTES.DASHBOARD}>{t('menu.dashboard')}</Link>
          {t('tutorials.importAfter')}
        </p>
      </Card>
    </>
  )
}
