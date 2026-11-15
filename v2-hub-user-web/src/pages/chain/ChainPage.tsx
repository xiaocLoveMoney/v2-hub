import { useEffect, useState } from 'react'
import { api, type ChainKind, type ChainOverview } from '../../api/client'
import { Alert, Card, PageHeader, Segmented, type SegmentedOption } from '../../components/ui'
import { useRemote } from '../../hooks/use-remote'
import { useI18n, type MessageKey } from '../../i18n/i18n-context'
import { ChainFlowCard } from './ChainFlowCard'
import { ExitsCard } from './ExitsCard'
import { KIND_SERVER } from './labels'
import { RouteList } from './RouteList'

const KIND_CLIENT: ChainKind = 'client'
/** 有起飞节点在下发中时自动刷新的间隔（docs/06 §5 M7）。 */
const PENDING_REFRESH_MS = 15_000
const STATE_PENDING: ChainOverview['serverRoutes'][number]['entries'][number]['state'] = 'pending'

const loadChain = (signal: AbortSignal) => api.GET('/api/user/chain', { signal })

/** 说明卡的条目，按显示顺序。 */
const NOTE_KEYS: readonly MessageKey[] = ['chain.note1', 'chain.note2', 'chain.note3', 'chain.note4', 'chain.note5', 'chain.note6']

/** 说明卡：两种链式的区别、落地的种类、状态的含义与流量怎么计（Ruling-36 – Ruling-39、Ruling-47）。 */
function NotesCard() {
  const { t } = useI18n()
  return (
    <Card title={t('chain.notesTitle')}>
      <ul className="hub-notes">
        {NOTE_KEYS.map((key) => (
          <li key={key}>{t(key)}</li>
        ))}
      </ul>
    </Card>
  )
}

/** 默认页签：套餐开了服务端链式或已有服务端规则时先看服务端，否则看客户端。 */
function initialKind(overview: ChainOverview): ChainKind {
  const hasServer = overview.serverAllowed || overview.serverRoutes.length > 0
  const hasClient = overview.allowed || overview.clientRoutes.length > 0
  return hasServer || !hasClient ? KIND_SERVER : KIND_CLIENT
}

interface ChainTabsProps {
  overview: ChainOverview
  kind: ChainKind
  onKindChange: (kind: ChainKind) => void
  onChanged: () => void
}

/** 两个页签：服务端链式、客户端链式，各是一张有序的规则列表。 */
function ChainTabs({ overview, kind, onKindChange, onChanged }: ChainTabsProps) {
  const { t } = useI18n()
  const tabs: SegmentedOption<ChainKind>[] = [
    { value: KIND_SERVER, label: t('chain.tabServer', { count: overview.serverRoutes.length, max: overview.maxServerRoutes }) },
    { value: KIND_CLIENT, label: t('chain.tabClient', { count: overview.clientRoutes.length, max: overview.maxClientRoutes }) },
  ]
  return (
    <>
      <Segmented aria="tab" className="hub-chain-tabs" aria-label={t('chain.tabsLabel')} options={tabs} value={kind} onChange={onKindChange} />
      <RouteList key={kind} overview={overview} kind={kind} onChanged={onChanged} />
    </>
  )
}

/** 链式代理（docs/03 §5.18、§5.19、§5.21，docs/06 §5 M9）：一个接口取全，任何写操作成功后整体重取。 */
export function ChainPage() {
  const { t } = useI18n()
  const chain = useRemote<ChainOverview>(loadChain)
  const overview = chain.data
  const reload = chain.reload
  const hasPending = overview?.serverRoutes.some((r) => r.entries.some((e) => e.state === STATE_PENDING)) ?? false

  // 有起飞节点在下发中时定时重取；没有了或页面卸载即停
  useEffect(() => {
    if (!hasPending) {
      return undefined
    }
    const timer = window.setInterval(reload, PENDING_REFRESH_MS)
    return () => window.clearInterval(timer)
  }, [hasPending, reload])

  return (
    <>
      <PageHeader title={t('menu.chain')} />
      {chain.error !== null && <Alert tone="plain">{chain.error.message}</Alert>}
      {overview !== null && !overview.allowed && !overview.serverAllowed && <Alert tone="warning">{t('chain.planNotAllowed')}</Alert>}
      {overview !== null && <ChainBody overview={overview} onChanged={reload} />}
    </>
  )
}

/** 页签与规则列表、出口与说明、链路示意；页签选择由链路示意卡共用。 */
function ChainBody({ overview, onChanged }: { overview: ChainOverview; onChanged: () => void }) {
  const [kind, setKind] = useState<ChainKind>(() => initialKind(overview))
  return (
    <>
      <ChainTabs overview={overview} kind={kind} onKindChange={setKind} onChanged={onChanged} />
      <div className="hub-grid-2">
        <ExitsCard overview={overview} onChanged={onChanged} />
        <NotesCard />
      </div>
      <ChainFlowCard kind={kind} routes={kind === KIND_SERVER ? overview.serverRoutes : overview.clientRoutes} />
    </>
  )
}
