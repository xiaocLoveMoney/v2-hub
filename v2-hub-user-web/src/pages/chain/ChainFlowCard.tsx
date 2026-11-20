import { lazy, Suspense, useMemo, useState } from 'react'
import { api, type ChainKind, type ChainOverview, type ChainRoute, type Dashboard } from '../../api/client'
import { useToast } from '../../components/toast-context'
import { Alert, Card, Empty, Loading } from '../../components/ui'
import { useRemote } from '../../hooks/use-remote'
import { useI18n } from '../../i18n/i18n-context'
import { KIND_SERVER } from './labels'
import { RouteModal } from './RouteModal'

const loadDashboard = (signal: AbortSignal) => api.GET('/api/user/dashboard', { signal })

/** 画布加载失败（多半是发版后旧页面取不到分包）：给一行提示，不让整页报错。 */
function CanvasLoadFailed() {
  const { t } = useI18n()
  return <Alert tone="warning">{t('chain.flowLoadFailed')}</Alert>
}

// React Flow 只在这张卡里用，按需加载，不进首屏包
const ChainFlowCanvas = lazy(() => import('./ChainFlowCanvas').catch(() => ({ default: CanvasLoadFailed })))

interface ChainFlowCardProps {
  overview: ChainOverview
  kind: ChainKind
  onChanged: () => void
}

/**
 * 链路示意卡（docs/06 链路示意卡，Ruling-91）：跟随当前页签，把规则画在可拖动的画布上——起飞节点按落地分组排成网格，
 * 生效的连线为实线并流动，其余为灰色虚线；点节点、区域或连线编辑相关规则。纯前端按已有数据绘制。
 */
export function ChainFlowCard({ overview, kind, onChanged }: ChainFlowCardProps) {
  const { t } = useI18n()
  const toast = useToast()
  const dashboard = useRemote<Dashboard>(loadDashboard)
  const [editing, setEditing] = useState<ChainRoute | null>(null)
  const isServer = kind === KIND_SERVER
  const routes = isServer ? overview.serverRoutes : overview.clientRoutes
  const isWritable = isServer ? overview.serverAllowed : overview.allowed
  const regions = useMemo(() => new Map(overview.nodes.map((n) => [n.id, n.region])), [overview.nodes])
  const title = t(isServer ? 'chain.flowTitleServer' : 'chain.flowTitleClient')
  if (routes.length === 0) {
    return (
      <Card title={title}>
        <Empty>{t('chain.flowEmpty')}</Empty>
      </Card>
    )
  }

  const online = dashboard.data?.onlineDevices
  const deviceSub = online === null || online === undefined ? t('chain.flowDeviceUnknown') : t('chain.flowDeviceOnline', { n: online })

  return (
    <Card title={title}>
      <p className="hub-field-hint">{t('chain.flowHint')}</p>
      <Suspense fallback={<Loading />}>
        <ChainFlowCanvas key={kind} kind={kind} routes={routes} regions={regions} deviceSub={deviceSub} isWritable={isWritable} onEdit={setEditing} isEditing={editing !== null} />
      </Suspense>
      <div className="hub-flow-legend">
        <span className="hub-flow-legend-item">
          <span className="hub-flow-swatch is-active" aria-hidden="true" />
          {t('chain.flowLegendActive')}
        </span>
        <span className="hub-flow-legend-item">
          <span className="hub-flow-swatch" aria-hidden="true" />
          {t('chain.flowLegendInactive')}
        </span>
      </div>
      {/* 每次打开都重新挂载，表单状态不残留上一次的输入 */}
      {editing !== null && (
        <RouteModal
          key={editing.id}
          overview={overview}
          kind={kind}
          route={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            toast.show(t('chain.saved'), 'success')
            onChanged()
          }}
        />
      )}
    </Card>
  )
}
