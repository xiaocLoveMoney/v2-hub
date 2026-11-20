import { Empty } from 'antd'
import { useMemo, useState } from 'react'
import { api, type Server, type ServerTrend } from '../../../api/client'
import { useSession } from '../../../auth/session-context'
import { ErrorAlert } from '../../../components/ErrorAlert'
import { SERVER_STATUS_POLL_INTERVAL_MS, usePageVisible, usePollRemote } from '../../../hooks/use-polling'
import { useRemote } from '../../../hooks/use-remote'
import { PERMISSIONS } from '../../../menu'
import { PlainHttpTag } from '../status'
import { ServerReachCell } from '../server-reach-cell'
import { ServerBillingModal } from './ServerBillingModal'
import { ServerStatusCard } from './ServerStatusCard'

const loadServers = (signal: AbortSignal) => api.GET('/api/admin/servers', { signal })
const loadTrends = (signal: AbortSignal) => api.GET('/api/admin/servers/trends', { signal })

/**
 * 服务器页的“运行状态”页签（Ruling-87）：每台服务器一张状态卡片，只看不管——接入、同步、编辑、移除在“管理”页签；
 * 卡片上只保留账单与流量额度（server:write）。只在打开这个页签时挂载，页面在前台时每 30 秒刷新。
 */
export function ServerStatusBoard() {
  const { can } = useSession()
  const servers = useRemote<Server[]>(loadServers)
  const trends = useRemote<ServerTrend[]>(loadTrends)
  const [billing, setBilling] = useState<Server | null>(null)
  usePollRemote([servers, trends], usePageVisible(), SERVER_STATUS_POLL_INTERVAL_MS)
  const trendById = useMemo(() => new Map((trends.data ?? []).map((t) => [t.serverId, t])), [trends.data])
  // 延迟检测开着时接口里每台都带 latency（Ruling-86）；拿不到近况时不出延迟区
  const hasLatency = (trends.data ?? []).some((t) => t.latency !== null)
  return (
    <>
      <ErrorAlert error={servers.error} />
      <div className="server-card-grid-list">
        {(servers.data ?? []).map((s) => (
          <ServerStatusCard
            key={s.id}
            server={s}
            trend={trendById.get(s.id)}
            hasLatency={hasLatency}
            canEditBilling={can(PERMISSIONS.SERVER_WRITE)}
            onEditBilling={setBilling}
            badges={
              <>
                <PlainHttpTag baseUrl={s.baseUrl} />
                {s.reach !== null && s.reach.total > 0 && <ServerReachCell reach={s.reach} />}
              </>
            }
          />
        ))}
        {servers.data !== null && servers.data.length === 0 && <Empty description="还没有接入服务器" />}
      </div>
      <ServerBillingModal
        server={billing}
        onClose={() => setBilling(null)}
        onSaved={() => {
          setBilling(null)
          servers.reload()
        }}
      />
    </>
  )
}
