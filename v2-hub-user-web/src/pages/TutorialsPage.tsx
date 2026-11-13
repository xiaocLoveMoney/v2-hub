import { useState } from 'react'
import { Link } from 'react-router'
import { api, type Tutorial, type TutorialPlatform } from '../api/client'
import { Markdown } from '../components/Markdown'
import { Alert, Card, Empty, PageHeader } from '../components/ui'
import { useRemote } from '../hooks/use-remote'
import { ROUTES } from '../menu'
import { useI18n, type MessageKey } from '../i18n/i18n-context'

/** 平台的显示名与显示顺序；取值见 hub-api.yaml 的 TutorialPlatform。 */
const PLATFORMS: { value: TutorialPlatform; labelKey: MessageKey }[] = [
  { value: 'windows', labelKey: 'tutorials.windows' },
  { value: 'macos', labelKey: 'tutorials.macos' },
  { value: 'android', labelKey: 'tutorials.android' },
  { value: 'ios', labelKey: 'tutorials.ios' },
  { value: 'linux', labelKey: 'tutorials.linux' },
  { value: 'router', labelKey: 'tutorials.router' },
]
// 后端已保证链接是 https；前端再判一次，不合规的不渲染成可点的链接
const HTTPS_PREFIX = 'https://'

const loadTutorials = (signal: AbortSignal) => api.GET('/api/user/tutorials', { signal })

function TutorialCard({ tutorial }: { tutorial: Tutorial }) {
  const { t } = useI18n()
  const links = tutorial.links.filter((l) => l.url.startsWith(HTTPS_PREFIX))
  return (
    <Card title={tutorial.clientName}>
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
  )
}

/** 下载和使用（docs/03 §5.9）：按操作系统分段，每篇教程一张卡。只显示有教程的平台。 */
export function TutorialsPage() {
  const { t } = useI18n()
  const tutorials = useRemote<Tutorial[]>(loadTutorials)
  const [picked, setPicked] = useState<TutorialPlatform | null>(null)
  const all = tutorials.data ?? []
  const platforms = PLATFORMS.filter((p) => all.some((item) => item.platform === p.value))
  // 没选过或选中的平台已没有教程时，落到第一个有教程的平台
  const current = platforms.some((p) => p.value === picked) ? picked : (platforms[0]?.value ?? null)

  return (
    <>
      <PageHeader title={t('menu.tutorials')} />
      {tutorials.error !== null && <Alert tone="plain">{tutorials.error.message}</Alert>}
      {tutorials.data !== null && platforms.length === 0 && (
        <Card>
          <Empty>{t('tutorials.empty')}</Empty>
        </Card>
      )}
      {platforms.length > 0 && (
        <div className="hub-pills" role="tablist" aria-label={t('tutorials.os')}>
          {platforms.map((p) => (
            <button
              key={p.value}
              type="button"
              role="tab"
              aria-selected={p.value === current}
              className={`hub-pill${p.value === current ? ' is-active' : ''}`}
              onClick={() => setPicked(p.value)}
            >
              {t(p.labelKey)}
            </button>
          ))}
        </div>
      )}
      {all
        .filter((item) => item.platform === current)
        .map((item) => (
          <TutorialCard key={item.id} tutorial={item} />
        ))}
    </>
  )
}
