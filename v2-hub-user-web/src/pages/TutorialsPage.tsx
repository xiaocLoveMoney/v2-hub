import { ChevronRight } from 'lucide-react'
import { Link, useSearchParams } from 'react-router'
import type { Tutorial, TutorialPlatform } from '../api/client'
import { Alert, Card, Empty, PageHeader } from '../components/ui'
import { useRemote } from '../hooks/use-remote'
import { useI18n } from '../i18n/i18n-context'
import { loadTutorials, parsePlatform, PLATFORM_PARAM, PLATFORMS, tutorialDetailPath } from './tutorials'

const ARROW_SIZE = 16

/** 一篇教程的概要卡（Ruling-90）：只有客户端名与简介，整张卡可点，进详情页看下载链接与正文。 */
function TutorialSummaryCard({ tutorial }: { tutorial: Tutorial }) {
  const { t } = useI18n()
  return (
    <Link className="hub-tutorial-card" to={tutorialDetailPath(tutorial.id)}>
      <h4>{tutorial.clientName}</h4>
      {tutorial.summary !== '' && <p className="hub-tutorial-summary">{tutorial.summary}</p>}
      <span className="hub-tutorial-more">
        {t('tutorials.view')}
        <ChevronRight size={ARROW_SIZE} aria-hidden />
      </span>
    </Link>
  )
}

/** 下载和使用（docs/03 §5.9）：按操作系统分段，每篇教程一张概要卡。只显示有教程的平台；选中的平台记在地址里。 */
export function TutorialsPage() {
  const { t } = useI18n()
  const tutorials = useRemote<Tutorial[]>(loadTutorials)
  const [searchParams, setSearchParams] = useSearchParams()
  const picked = parsePlatform(searchParams.get(PLATFORM_PARAM))
  const all = tutorials.data ?? []
  const platforms = PLATFORMS.filter((p) => all.some((item) => item.platform === p.value))
  // 没选过或选中的平台已没有教程时，落到第一个有教程的平台
  const current = platforms.some((p) => p.value === picked) ? picked : (platforms[0]?.value ?? null)

  const handlePick = (platform: TutorialPlatform) => {
    // 切平台不留历史记录，返回键直接回到上一页面
    setSearchParams({ [PLATFORM_PARAM]: platform }, { replace: true })
  }

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
              onClick={() => handlePick(p.value)}
            >
              {t(p.labelKey)}
            </button>
          ))}
        </div>
      )}
      <div className="hub-tutorial-grid">
        {all
          .filter((item) => item.platform === current)
          .map((item) => (
            <TutorialSummaryCard key={item.id} tutorial={item} />
          ))}
      </div>
    </>
  )
}
