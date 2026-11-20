import { PlusOutlined } from '@ant-design/icons'
import { App, Button, Empty, Popconfirm, Segmented, Space, Table, Tag, type TableProps } from 'antd'
import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { api, csrfHeader, toApiError, type Server, type ServerTrend } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { mutate, useRemote } from '../../hooks/use-remote'
import { SERVER_STATUS_POLL_INTERVAL_MS, usePageVisible, usePollRemote } from '../../hooks/use-polling'
import { ServerBillingModal } from './server-card/ServerBillingModal'
import { ServerStatusCard } from './server-card/ServerStatusCard'
import { formatPercent, percentOf } from './server-card/server-card-format'
import { PERMISSIONS } from '../../menu'
import { TABLE_SCROLL, formatTime } from '../system/format'
import { ServerFormModal } from './ServerFormModal'
import { PlainHttpTag, ServerStatusTag } from './status'
import { formatCountry } from './countries'
import { ErrorAlert } from '../../components/ErrorAlert'
import { PageHeader } from '../../components/PageHeader'

const loadServers = (signal: AbortSignal) => api.GET('/api/admin/servers', { signal })
const loadTrends = (signal: AbortSignal) => api.GET('/api/admin/servers/trends', { signal })
/** 列表的显示方式记在本机浏览器里（只是个人偏好，读写失败按默认的卡片）。 */
const VIEW_STORAGE_KEY = 'v2hub.admin.servers.view'
const VIEW_CARDS = 'cards'
const VIEW_TABLE = 'table'
type ViewMode = typeof VIEW_CARDS | typeof VIEW_TABLE
const VIEW_OPTIONS: { value: ViewMode; label: string }[] = [
  { value: VIEW_CARDS, label: '卡片' },
  { value: VIEW_TABLE, label: '表格' },
]

function readViewMode(): ViewMode {
  try {
    return localStorage.getItem(VIEW_STORAGE_KEY) === VIEW_TABLE ? VIEW_TABLE : VIEW_CARDS
  } catch (err) {
    reportError(err)
    return VIEW_CARDS
  }
}

function saveViewMode(mode: ViewMode) {
  try {
    localStorage.setItem(VIEW_STORAGE_KEY, mode)
  } catch (err) {
    reportError(err)
  }
}
/** “大陆连通”列插在“状态”列后面。 */
const REACH_COLUMN_INDEX = 3
const EMPTY = '—'

/** 一台服务器上大陆连通检测的概况：有被墙或部分不通的地址就标出个数；否则说明有几个地址测出来是正常的。 */
function ServerReachCell({ reach }: { reach: Server['reach'] }) {
  if (reach === null || reach.total === 0) {
    return <>{EMPTY}</>
  }
  if (reach.blocked === 0 && reach.partial === 0) {
    if (reach.ok === 0) {
      // 没有一个地址测出正常：还没测、未知或端口不通，细节在节点页
      return <Tag>暂无结论</Tag>
    }
    return <Tag color="green">{reach.ok === reach.total ? '正常' : `正常 ${reach.ok}/${reach.total}`}</Tag>
  }
  return (
    <Space size={4}>
      {reach.blocked > 0 && <Tag color="red">被墙 {reach.blocked}</Tag>}
      {reach.partial > 0 && <Tag color="orange">部分不通 {reach.partial}</Tag>}
    </Space>
  )
}

/** 服务器管理（server:read 查看，server:write 接入 / 编辑 / 同步 / 移除 / 账单）；默认以状态卡片显示，可切到表格（Ruling-83）。 */
export function ServersPage() {
  const { can } = useSession()
  const { message } = App.useApp()
  const canWrite = can(PERMISSIONS.SERVER_WRITE)
  const servers = useRemote<Server[]>(loadServers)
  const trends = useRemote<ServerTrend[]>(loadTrends)
  const [viewMode, setViewMode] = useState<ViewMode>(readViewMode)
  const [billing, setBilling] = useState<Server | null>(null)
  const trendById = useMemo(() => new Map((trends.data ?? []).map((t) => [t.serverId, t])), [trends.data])
  // 延迟检测开着时接口里每台都带 latency（Ruling-86）；拿不到趋势时不出延迟区
  const hasLatency = (trends.data ?? []).some((t) => t.latency !== null)
  // 页面在前台时定时刷新状态（切到后台不刷）
  usePollRemote([servers, trends], usePageVisible(), SERVER_STATUS_POLL_INTERVAL_MS)
  const [editing, setEditing] = useState<Server | null>(null)
  const [isCreating, setIsCreating] = useState(false)
  const [syncingId, setSyncingId] = useState<number | null>(null)

  const close = () => {
    setEditing(null)
    setIsCreating(false)
  }

  const handleSync = async (srv: Server) => {
    setSyncingId(srv.id)
    try {
      const { data, error } = await api.POST('/api/admin/servers/{id}/sync', { params: { ...csrfHeader(), path: { id: srv.id } } })
      if (error !== undefined || data?.data === undefined) {
        void message.error(toApiError(error).message)
        return
      }
      const r = data.data
      void message.success(`同步完成：面板 ${r.inbounds} 个入站，新增 ${r.added}，漂移 ${r.drifted}，缺失 ${r.missing}`)
    } catch (err) {
      reportError(err)
      void message.error(toApiError(err).message)
    } finally {
      setSyncingId(null)
      servers.reload()
    }
  }

  const handleDelete = async (srv: Server) => {
    const err = await mutate(() => api.DELETE('/api/admin/servers/{id}', { params: { ...csrfHeader(), path: { id: srv.id } } }))
    if (err !== null) {
      void message.error(err.message)
      return
    }
    void message.success('已移除（面板本身未改动）')
    servers.reload()
  }

  const columns: TableProps<Server>['columns'] = [
    {
      title: '名称',
      dataIndex: 'name',
      render: (name: string, s: Server) => (
        <Space size={4}>
          <Link to={`/servers/${s.id}`}>{name}</Link>
          <PlainHttpTag baseUrl={s.baseUrl} />
        </Space>
      ),
    },
    { title: '位置', dataIndex: 'region', render: (v: string) => formatCountry(v) },
    { title: '状态', key: 'status', render: (_: unknown, s: Server) => <ServerStatusTag status={s.status} lastError={s.lastError} isDisabled={s.isDisabled} /> },
    {
      title: 'CPU / 内存',
      key: 'resources',
      render: (_: unknown, s: Server) =>
        s.resources === null ? EMPTY : `${s.resources.cpu === null ? EMPTY : formatPercent(s.resources.cpu)} / ${formatPercent(percentOf(s.resources.memUsed, s.resources.memTotal))}`,
    },
    { title: '面板版本', dataIndex: 'panelVersion' },
    { title: '入站', dataIndex: 'inboundCount' },
    { title: '最近在线', dataIndex: 'lastSeenAt', render: (v: string | null | undefined) => formatTime(v) },
    { title: '最近同步', dataIndex: 'lastSyncedAt', render: (v: string | null | undefined) => formatTime(v) },
  ]
  // 大陆连通检测开着时接口才带 reach（Ruling-67）；没开就不出这一列
  const hasReach = (servers.data ?? []).some((s) => s.reach !== null)
  if (hasReach) {
    columns.splice(REACH_COLUMN_INDEX, 0, { title: '大陆连通', key: 'reach', render: (_: unknown, s: Server) => <ServerReachCell reach={s.reach} /> })
  }
  const renderActions = (s: Server) => (
    <Space>
      <Button size="small" loading={syncingId === s.id} disabled={s.isDisabled} onClick={() => void handleSync(s)}>
        同步
      </Button>
      <Button size="small" onClick={() => setEditing(s)}>
        编辑
      </Button>
      <Popconfirm title={`移除服务器 ${s.name}？`} description="只删除平台记录（入站缓存、节点），不改动面板" onConfirm={() => handleDelete(s)}>
        <Button size="small" danger>
          移除
        </Button>
      </Popconfirm>
    </Space>
  )
  if (canWrite) {
    columns.push({ title: '操作', key: 'actions', render: (_: unknown, s: Server) => renderActions(s) })
  }

  return (
    <>
      <PageHeader
        title="服务器"
        action={canWrite && (
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setIsCreating(true)}>
            接入服务器
          </Button>
        )}
      />
      <ErrorAlert error={servers.error} />
      <div className="server-view-switch">
        <Segmented<ViewMode>
          aria-label="显示方式"
          options={VIEW_OPTIONS}
          value={viewMode}
          onChange={(mode) => {
            setViewMode(mode)
            saveViewMode(mode)
          }}
        />
      </div>
      {viewMode === VIEW_TABLE ? (
        <Table<Server> rowKey="id" scroll={TABLE_SCROLL} loading={servers.isLoading && servers.data === null} columns={columns} dataSource={servers.data ?? []} pagination={false} />
      ) : (
        <div className="server-card-grid-list">
          {(servers.data ?? []).map((s) => (
            <ServerStatusCard
              key={s.id}
              server={s}
              trend={trendById.get(s.id)}
              hasLatency={hasLatency}
              canEditBilling={canWrite}
              onEditBilling={setBilling}
              badges={
                <>
                  <PlainHttpTag baseUrl={s.baseUrl} />
                  {s.reach !== null && s.reach.total > 0 && <ServerReachCell reach={s.reach} />}
                </>
              }
              actions={canWrite ? renderActions(s) : undefined}
            />
          ))}
          {servers.data !== null && servers.data.length === 0 && <Empty description="还没有接入服务器" />}
        </div>
      )}
      <ServerBillingModal
        server={billing}
        onClose={() => setBilling(null)}
        onSaved={() => {
          setBilling(null)
          servers.reload()
        }}
      />
      <ServerFormModal
        server={editing}
        isOpen={isCreating || editing !== null}
        onClose={close}
        onSaved={() => {
          close()
          servers.reload()
        }}
      />
    </>
  )
}
