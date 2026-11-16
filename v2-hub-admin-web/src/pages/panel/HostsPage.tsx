import { PlusOutlined } from '@ant-design/icons'
import { App, Button, Popconfirm, Space, Table, Typography, type TableProps } from 'antd'
import { useState } from 'react'
import { Link } from 'react-router'
import { api, csrfHeader, type HostAction, type HostTask, type ServerHost } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { ErrorAlert } from '../../components/ErrorAlert'
import { PageHeader } from '../../components/PageHeader'
import { usePollRemote } from '../../hooks/use-polling'
import { useRemote } from '../../hooks/use-remote'
import { useRowAction } from '../../hooks/use-submit'
import { PERMISSIONS, ROUTES } from '../../menu'
import { TABLE_SCROLL } from '../system/format'
import { DnsAccountsModal } from './DnsAccountsModal'
import { HostCreateModal } from './HostCreateModal'
import { HostEditModal } from './HostEditModal'
import { HostTaskDrawer } from './HostTaskDrawer'
import { HostTaskSummary } from './HostTaskSummary'
import { SshCredentialsModal } from './SshCredentialsModal'
import { startHostAction, type OpenedHostTasks } from './host-actions'
import { EMPTY, certExpiryText, isHostTaskActive } from './host-labels'
import { formatHostEndpoint } from './host-list'

const loadHosts = (signal: AbortSignal) => api.GET('/api/admin/server-hosts', { signal })
const NOT_ONBOARDED = '未接入'
/** 部署动作：只对还没关联服务器的主机可用（docs/03 §5.22 任务）。 */
const ACTION_PROVISION: HostAction = 'provision'

/** 系统 / 架构：都还没检测到时显示占位符。 */
function formatPlatform(host: ServerHost): string {
  if (host.osId === '' && host.arch === '') {
    return EMPTY
  }
  return `${host.osId || EMPTY} / ${host.arch || EMPTY}`
}

/** 域名与证书：没配 DNS 账号的主机显示占位符；配了的显示域名，以及证书到期日或“还没申请到”。 */
function HostDomainCell({ host }: { host: ServerHost }) {
  if (host.domain === '') {
    return <>{EMPTY}</>
  }
  return (
    <Space orientation="vertical" size={0}>
      <span className="mono">{host.domain}</span>
      <Typography.Text type="secondary">{certExpiryText(host.certNotAfter)}</Typography.Text>
    </Space>
  )
}

interface HostRowActionsProps {
  host: ServerHost
  canWrite: boolean
  onOpenTasks: () => void
  onProvision: () => Promise<void>
  onEdit: () => void
  onResetHostKey: () => Promise<unknown>
  onDelete: () => Promise<unknown>
}

/** 一台主机的行内操作；写操作只在有 host:write 时出现，主机有未结束的任务时部署、编辑、删除不可点（后端会回 409）。 */
function HostRowActions({ host, canWrite, onOpenTasks, onProvision, onEdit, onResetHostKey, onDelete }: HostRowActionsProps) {
  const isBusy = isHostTaskActive(host.lastTask)
  return (
    <Space>
      <Button size="small" onClick={onOpenTasks}>
        查看任务
      </Button>
      {canWrite && host.serverId === null && (
        <Popconfirm
          title={`在 ${host.name} 上部署面板？`}
          description="平台经 SSH 登录这台机器：没有面板就安装，已有标准安装的面板就接管，然后接入为服务器"
          onConfirm={onProvision}
        >
          <Button size="small" type="primary" disabled={isBusy}>
            {host.lastTask === null ? '部署' : '重新部署'}
          </Button>
        </Popconfirm>
      )}
      {canWrite && (
        <>
          <Button size="small" disabled={isBusy} onClick={onEdit}>
            编辑
          </Button>
          <Popconfirm
            title={`重置 ${host.name} 的主机密钥？`}
            description="清空已固定的主机密钥，下次连接时重新记录。只在确认机器确实重装过之后做"
            onConfirm={onResetHostKey}
          >
            <Button size="small">重置主机密钥</Button>
          </Popconfirm>
          <Popconfirm
            title={`删除主机 ${host.name}？`}
            description="只删除平台里的 SSH 记录与任务历史，不动这台机器，也不删已关联的服务器"
            onConfirm={onDelete}
          >
            <Button size="small" danger disabled={isBusy}>
              删除
            </Button>
          </Popconfirm>
        </>
      )}
    </Space>
  )
}

/** SSH 托管：主机列表（host:read 查看，host:write 新建、部署、编辑、重置主机密钥、删除；docs/03 §5.22）。 */
export function HostsPage() {
  const { can } = useSession()
  const { message } = App.useApp()
  const canWrite = can(PERMISSIONS.HOST_WRITE)
  // 服务器详情页的路由要 node:read，没有就只显示名字
  const canOpenServer = can(PERMISSIONS.NODE_READ)
  const hosts = useRemote<ServerHost[]>(loadHosts)
  const [isCreating, setIsCreating] = useState(false)
  const [isCredentialsOpen, setIsCredentialsOpen] = useState(false)
  const [isDnsAccountsOpen, setIsDnsAccountsOpen] = useState(false)
  const [editing, setEditing] = useState<ServerHost | null>(null)
  const [opened, setOpened] = useState<OpenedHostTasks | null>(null)
  const act = useRowAction(hosts.reload)

  // 有主机的任务还没结束时每 2 秒刷新列表，全部结束即停
  const hasActiveTask = (hosts.data ?? []).some((h) => isHostTaskActive(h.lastTask))
  usePollRemote([hosts], hasActiveTask)

  const openTasks = (host: ServerHost, task: HostTask | null) => setOpened({ host: { id: host.id, name: host.name }, taskId: task?.id ?? null })

  const handleProvision = async (host: ServerHost) => {
    const result = await startHostAction(host.id, ACTION_PROVISION)
    if (result.error !== null) {
      void message.error(result.error.message)
      return
    }
    void message.success('部署任务已排队')
    openTasks(host, result.task)
    hosts.reload()
  }

  const hostPath = (host: ServerHost) => ({ params: { ...csrfHeader(), path: { id: host.id } } })
  const columns: TableProps<ServerHost>['columns'] = [
    { title: '名称', dataIndex: 'name' },
    { title: '地址', key: 'address', render: (_: unknown, h: ServerHost) => <span className="mono">{formatHostEndpoint(h.address, h.sshPort)}</span> },
    { title: '凭据', dataIndex: 'credentialName' },
    { title: '系统 / 架构', key: 'platform', render: (_: unknown, h: ServerHost) => formatPlatform(h) },
    { title: '域名 / 证书', key: 'domain', render: (_: unknown, h: ServerHost) => <HostDomainCell host={h} /> },
    {
      title: '关联服务器',
      key: 'server',
      render: (_: unknown, h: ServerHost) => {
        if (h.serverId === null) {
          return NOT_ONBOARDED
        }
        return canOpenServer ? <Link to={`${ROUTES.SERVERS}/${h.serverId}`}>{h.serverName}</Link> : h.serverName
      },
    },
    { title: '最近任务', key: 'lastTask', render: (_: unknown, h: ServerHost) => <HostTaskSummary task={h.lastTask} onOpen={(task) => openTasks(h, task)} /> },
    {
      title: '操作',
      key: 'actions',
      render: (_: unknown, h: ServerHost) => (
        <HostRowActions
          host={h}
          canWrite={canWrite}
          onOpenTasks={() => openTasks(h, null)}
          onProvision={() => handleProvision(h)}
          onEdit={() => setEditing(h)}
          onResetHostKey={() => act(() => api.POST('/api/admin/server-hosts/{id}/reset-host-key', hostPath(h)), '已重置主机密钥')}
          onDelete={() => act(() => api.DELETE('/api/admin/server-hosts/{id}', hostPath(h)), '已删除（目标机未改动）')}
        />
      ),
    },
  ]

  return (
    <>
      <PageHeader
        title="SSH 托管"
        action={
          <Space>
            <Button onClick={() => setIsCredentialsOpen(true)}>SSH 凭据</Button>
            <Button onClick={() => setIsDnsAccountsOpen(true)}>DNS 账号</Button>
            {canWrite && (
              <Button type="primary" icon={<PlusOutlined />} onClick={() => setIsCreating(true)}>
                新建主机
              </Button>
            )}
          </Space>
        }
      />
      <ErrorAlert error={hosts.error} />
      <Table<ServerHost>
        rowKey="id"
        scroll={TABLE_SCROLL}
        // 轮询刷新时不闪加载态
        loading={hosts.isLoading && hosts.data === null}
        columns={columns}
        dataSource={hosts.data ?? []}
        pagination={false}
      />
      <HostCreateModal
        isOpen={isCreating}
        onClose={() => setIsCreating(false)}
        onSaved={() => {
          setIsCreating(false)
          hosts.reload()
        }}
      />
      <HostEditModal
        host={editing}
        onClose={() => setEditing(null)}
        onSaved={() => {
          setEditing(null)
          hosts.reload()
        }}
      />
      <SshCredentialsModal isOpen={isCredentialsOpen} canWrite={canWrite} onClose={() => setIsCredentialsOpen(false)} onChanged={hosts.reload} />
      <DnsAccountsModal isOpen={isDnsAccountsOpen} canWrite={canWrite} onClose={() => setIsDnsAccountsOpen(false)} onChanged={hosts.reload} />
      <HostTaskDrawer
        host={opened?.host ?? null}
        initialTaskId={opened?.taskId}
        onClose={() => {
          setOpened(null)
          hosts.reload()
        }}
        onChanged={hosts.reload}
      />
    </>
  )
}
