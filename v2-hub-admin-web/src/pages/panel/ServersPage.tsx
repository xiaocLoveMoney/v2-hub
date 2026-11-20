import { PlusOutlined } from '@ant-design/icons'
import { App, Button, Popconfirm, Space, Table, Tabs, type TableProps } from 'antd'
import { useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { api, csrfHeader, toApiError, type Server } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { mutate, useRemote } from '../../hooks/use-remote'
import { ServerStatusBoard } from './server-card/ServerStatusBoard'
import { formatPercent, percentOf } from './server-card/server-card-format'
import { ServerReachCell } from './server-reach-cell'
import { PERMISSIONS } from '../../menu'
import { TABLE_SCROLL, formatTime } from '../system/format'
import { ServerFormModal } from './ServerFormModal'
import { PlainHttpTag, ServerStatusTag } from './status'
import { formatCountry } from './countries'
import { ErrorAlert } from '../../components/ErrorAlert'
import { PageHeader } from '../../components/PageHeader'

const loadServers = (signal: AbortSignal) => api.GET('/api/admin/servers', { signal })
/** 页签（Ruling-87）记在地址栏的 ?tab= 里：默认“管理”，“运行状态”是只读的卡片墙。 */
const TAB_PARAM = 'tab'
const TAB_MANAGE = 'manage'
const TAB_STATUS = 'status'
/** “大陆连通”列插在“状态”列后面。 */
const REACH_COLUMN_INDEX = 3
const EMPTY = '—'

/** 服务器（server:read 查看，server:write 接入 / 编辑 / 同步 / 移除）；“管理”页签是表格，“运行状态”页签是状态卡片（Ruling-87）。 */
export function ServersPage() {
  const { can } = useSession()
  const { message } = App.useApp()
  const canWrite = can(PERMISSIONS.SERVER_WRITE)
  const servers = useRemote<Server[]>(loadServers)
  const [searchParams, setSearchParams] = useSearchParams()
  const activeTab = searchParams.get(TAB_PARAM) === TAB_STATUS ? TAB_STATUS : TAB_MANAGE
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
      <Tabs
        activeKey={activeTab}
        destroyOnHidden
        onChange={(key) => setSearchParams(key === TAB_STATUS ? { [TAB_PARAM]: key } : {}, { replace: true })}
        items={[
          {
            key: TAB_MANAGE,
            label: '管理',
            children: (
              <>
                <ErrorAlert error={servers.error} />
                <Table<Server> rowKey="id" scroll={TABLE_SCROLL} loading={servers.isLoading} columns={columns} dataSource={servers.data ?? []} pagination={false} />
              </>
            ),
          },
          { key: TAB_STATUS, label: '运行状态', children: <ServerStatusBoard /> },
        ]}
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
