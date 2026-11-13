import { useState } from 'react'
import { Link } from 'react-router'
import { api, type Tutorial, type TutorialPlatform } from '../api/client'
import { Markdown } from '../components/Markdown'
import { Alert, Card, Empty, PageHeader } from '../components/ui'
import { useRemote } from '../hooks/use-remote'
import { ROUTES } from '../menu'

/** 平台的显示名与显示顺序；取值见 hub-api.yaml 的 TutorialPlatform。 */
const PLATFORMS: { value: TutorialPlatform; label: string }[] = [
  { value: 'windows', label: 'Windows' },
  { value: 'macos', label: 'macOS' },
  { value: 'android', label: 'Android' },
  { value: 'ios', label: 'iOS' },
  { value: 'linux', label: 'Linux' },
  { value: 'router', label: '路由器' },
]
// 后端已保证链接是 https；前端再判一次，不合规的不渲染成可点的链接
const HTTPS_PREFIX = 'https://'

const loadTutorials = (signal: AbortSignal) => api.GET('/api/user/tutorials', { signal })

function TutorialCard({ tutorial }: { tutorial: Tutorial }) {
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
        装好客户端后，到 <Link to={ROUTES.DASHBOARD}>仪表盘</Link> 一键导入订阅。
      </p>
    </Card>
  )
}

/** 下载和使用（docs/03 §5.9）：按操作系统分段，每篇教程一张卡。只显示有教程的平台。 */
export function TutorialsPage() {
  const tutorials = useRemote<Tutorial[]>(loadTutorials)
  const [picked, setPicked] = useState<TutorialPlatform | null>(null)
  const all = tutorials.data ?? []
  const platforms = PLATFORMS.filter((p) => all.some((t) => t.platform === p.value))
  // 没选过或选中的平台已没有教程时，落到第一个有教程的平台
  const current = platforms.some((p) => p.value === picked) ? picked : (platforms[0]?.value ?? null)

  return (
    <>
      <PageHeader title="下载和使用" />
      {tutorials.error !== null && <Alert tone="plain">{tutorials.error.message}</Alert>}
      {tutorials.data !== null && platforms.length === 0 && (
        <Card>
          <Empty>还没有教程</Empty>
        </Card>
      )}
      {platforms.length > 0 && (
        <div className="hub-pills" role="tablist" aria-label="操作系统">
          {platforms.map((p) => (
            <button
              key={p.value}
              type="button"
              role="tab"
              aria-selected={p.value === current}
              className={`hub-pill${p.value === current ? ' is-active' : ''}`}
              onClick={() => setPicked(p.value)}
            >
              {p.label}
            </button>
          ))}
        </div>
      )}
      {all
        .filter((t) => t.platform === current)
        .map((t) => (
          <TutorialCard key={t.id} tutorial={t} />
        ))}
    </>
  )
}
