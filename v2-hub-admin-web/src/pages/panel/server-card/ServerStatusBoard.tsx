import { Empty } from 'antd'
import { useMemo, useState } from 'react'
import { api, type Server, type ServerTrend } from '../../../api/client'
import { useSession } from '../../../auth/session-context'
import { ErrorAlert } from '../../../components/ErrorAlert'
import { SERVER_STATUS_POLL_INTERVAL_MS, usePageVisible, usePollRemote } from '../../../hooks/use-polling'
import { useRemote, type RemoteState } from '../../../hooks/use-remote'
import { PERMISSIONS } from '../../../menu'
import { ServerBillingModal } from './ServerBillingModal'
import { ServerStatusCard } from './ServerStatusCard'

const loadTrends = (signal: AbortSignal) => api.GET('/api/admin/servers/trends', { signal })

/**
 * 服务器页的“运行状态”页签（Ruling-88）：每台服务器一张状态卡片，只看不管——接入、同步、编辑、移除在“管理”页签；
 * 卡片上只保留账单与流量额度（server:write）。只在打开这个页签时挂载，页面在前台时每 30 秒刷新。
 * @param servers 服务器页已经拉到的服务器列表，轮询时一并刷新
 */
export function ServerStatusBoard({ servers }: { servers: RemoteState<Server[]> }) {
  const { can } = useSession()
  const trends = useRemote<ServerTrend[]>(loadTrends)
  const [billing, setBilling] = useState<Server | null>(null)
  usePollRemote([servers, trends], usePageVisible(), SERVER_STATUS_POLL_INTERVAL_MS)
  const trendById = useMemo(() => new Map((trends.data ?? []).map((t) => [t.serverId, t])), [trends.data])
  return (
    <>
      <ErrorAlert error={servers.error} />
      <div className="server-card-grid-list">
        {(servers.data ?? []).map((s) => (
          <ServerStatusCard
            key={s.id}
            server={s}
            trend={trendById.get(s.id)}
            canEditBilling={can(PERMISSIONS.SERVER_WRITE)}
            onEditBilling={setBilling}
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
