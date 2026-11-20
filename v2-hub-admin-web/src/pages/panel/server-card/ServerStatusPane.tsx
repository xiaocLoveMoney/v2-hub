import { useState } from 'react'
import { api, type Server, type ServerTrend } from '../../../api/client'
import { useSession } from '../../../auth/session-context'
import { useRemote } from '../../../hooks/use-remote'
import { PERMISSIONS } from '../../../menu'
import { ServerBillingModal } from './ServerBillingModal'
import { ServerStatusCard } from './ServerStatusCard'
import { ServerTrendCharts } from './ServerTrendCharts'

const loadTrends = (signal: AbortSignal) => api.GET('/api/admin/servers/trends', { signal })

interface ServerStatusPaneProps {
  server: Server
  /** 账单保存后通知详情页重新拉服务器。 */
  onChanged: () => void
}

/**
 * 服务器详情页的“运行状态”页签（Ruling-88）：状态卡片（细节展开）、账单与流量额度、24 小时 / 7 天趋势。
 * 只在打开这个页签时挂载，卡片上的速率小图与三网延迟取自列表的近况接口（Ruling-85、Ruling-86）。
 */
export function ServerStatusPane({ server, onChanged }: ServerStatusPaneProps) {
  const { can } = useSession()
  const trends = useRemote<ServerTrend[]>(loadTrends)
  const trend = trends.data?.find((t) => t.serverId === server.id)
  const [isEditingBilling, setIsEditingBilling] = useState(false)
  return (
    <>
      <div className="page-alert">
        <ServerStatusCard
          server={server}
          trend={trend}
          hasLatency={trend !== undefined && trend.latency !== null}
          canEditBilling={can(PERMISSIONS.SERVER_WRITE)}
          onEditBilling={() => setIsEditingBilling(true)}
          isDetail
        />
      </div>
      <ServerTrendCharts serverId={server.id} />
      <ServerBillingModal
        server={isEditingBilling ? server : null}
        onClose={() => setIsEditingBilling(false)}
        onSaved={() => {
          setIsEditingBilling(false)
          onChanged()
        }}
      />
    </>
  )
}
