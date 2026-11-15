import { PlusOutlined } from '@ant-design/icons'
import { App, Button, Input, InputNumber, Modal, Popconfirm, Select, Space, Tag, Typography, type TableProps } from 'antd'
import { useCallback, useState } from 'react'
import { api, csrfHeader, toApiError, type AdminChainExit, type ChainExitAssignMode, type ChainExitOwner, type NodeGroup, type Server } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { ErrorAlert } from '../../components/ErrorAlert'
import { PagedTable } from '../../components/PagedTable'
import { useRemote } from '../../hooks/use-remote'
import { useRowAction } from '../../hooks/use-submit'
import { PERMISSIONS } from '../../menu'
import { ChainExitModal } from './ChainExitModal'
import { ChainExitTestButton } from './ChainExitTestButton'
import { MAX_KEYWORD_LEN } from '../form-limits'
import { PageHeader } from '../../components/PageHeader'

const OWNER_PLATFORM: ChainExitOwner = 'platform'
const ASSIGN_USER: ChainExitAssignMode = 'user'
const NO_ROUTES = 0

const OWNER_OPTIONS: { value: ChainExitOwner; label: string }[] = [
  { value: 'platform', label: '平台出口' },
  { value: 'user', label: '用户出口' },
]

const loadGroups = (signal: AbortSignal) => api.GET('/api/admin/node-groups', { signal })
const loadServers = (signal: AbortSignal) => api.GET('/api/admin/servers', { signal })

/**
 * 链式出口（node:read 查看，node:write 增改删、停用与查看链接；docs/03 §5.21，Ruling-50）。
 * 平台出口由管理员添加，按节点组或指定用户授权（Ruling-64）；用户出口是用户自己添加的，这里只能停用 / 启用与删除，不能查看或修改它的链接。
 */
export function ChainExitsPage() {
  const { can } = useSession()
  const canWrite = can(PERMISSIONS.NODE_WRITE)
  const [owner, setOwner] = useState<ChainExitOwner | undefined>(undefined)
  const [userId, setUserId] = useState<number | undefined>(undefined)
  const [keyword, setKeyword] = useState('')
  const [refreshKey, setRefreshKey] = useState(0)
  const [editing, setEditing] = useState<AdminChainExit | null>(null)
  const [isOpen, setIsOpen] = useState(false)
  const groups = useRemote<NodeGroup[]>(loadGroups)
  // 延迟测试的起点：启用中的服务器（docs/03 §5.21）；列服务器要 server:read
  const canTest = canWrite && can(PERMISSIONS.SERVER_READ)
  // 没有 server:read 时不请求服务器列表，测试按钮也不出现
  const servers = useRemote<Server[]>(loadServers, canTest)
  const testServers = (servers.data ?? []).filter((s) => !s.isDisabled)
  const refresh = () => setRefreshKey((k) => k + 1)
  const act = useRowAction(refresh)

  const load = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) =>
      api.GET('/api/admin/chain-exits', { params: { query: { page, pageSize, owner, userId, keyword: keyword === '' ? undefined : keyword } }, signal }),
    [owner, userId, keyword],
  )
  const open = (e: AdminChainExit | null) => {
    setEditing(e)
    setIsOpen(true)
  }
  const setEnabled = (e: AdminChainExit, isEnabled: boolean) =>
    act(
      () => api.PUT('/api/admin/chain-exits/{id}/enabled', { params: { ...csrfHeader(), path: { id: e.id } }, body: { isEnabled } }),
      isEnabled ? '已启用' : '已停用',
    )
  const remove = (e: AdminChainExit) => act(() => api.DELETE('/api/admin/chain-exits/{id}', { params: { ...csrfHeader(), path: { id: e.id } } }), '已删除')

  const columns: TableProps<AdminChainExit>['columns'] = [
    { title: 'ID', dataIndex: 'id' },
    {
      title: '归属',
      key: 'owner',
      render: (_: unknown, e: AdminChainExit) =>
        e.user === null || e.user === undefined ? <Tag color="purple">平台出口</Tag> : <Tag color="gold">{`${e.user.username} #${e.user.id}`}</Tag>,
    },
    { title: '名称', dataIndex: 'name' },
    { title: '协议', dataIndex: 'protocol' },
    { title: '地址', key: 'address', render: (_: unknown, e: AdminChainExit) => <span className="mono">{`${e.address}:${e.port}`}</span> },
    { title: '分配', key: 'assign', render: (_: unknown, e: AdminChainExit) => <AssignCell exit={e} /> },
    { title: '可用于', key: 'usage', render: (_: unknown, e: AdminChainExit) => <UsageCell exit={e} /> },
    { title: '被引用', dataIndex: 'routeCount', render: (n: number) => `${n} 条规则` },
    { title: '状态', dataIndex: 'isEnabled', render: (isEnabled: boolean) => (isEnabled ? <Tag color="green">启用</Tag> : <Tag>已停用</Tag>) },
    { title: '备注', dataIndex: 'remark', render: (v: string) => v || '—' },
  ]
  if (canWrite) {
    columns.push({
      title: '操作',
      key: 'actions',
      render: (_: unknown, e: AdminChainExit) => (
        <RowActions exit={e} servers={canTest ? testServers : null} onEdit={() => open(e)} onToggle={() => setEnabled(e, !e.isEnabled)} onDelete={() => remove(e)} />
      ),
    })
  }

  return (
    <>
      <PageHeader
        title="链式出口"
        action={canWrite && (
          <Button type="primary" icon={<PlusOutlined />} onClick={() => open(null)}>
            新建平台出口
          </Button>
        )}
      />
      <Typography.Paragraph type="secondary">
        出口只用作链式规则的落地。平台出口可按节点组分配（套餐绑了该节点组的用户能用），也可指定给某些用户；用户出口由用户自己添加，这里可以停用或删除，但不能查看它的链接。
      </Typography.Paragraph>
      <ErrorAlert error={groups.error ?? servers.error} />
      <PagedTable<AdminChainExit>
        load={load}
        columns={columns}
        refreshKey={refreshKey}
        filters={
          <>
            <Select
              allowClear
              placeholder="按归属过滤"
              aria-label="按归属过滤"
              className="filter-select"
              options={OWNER_OPTIONS}
              value={owner}
              onChange={(v: ChainExitOwner | undefined) => setOwner(v)}
            />
            <InputNumber
              min={1}
              precision={0}
              placeholder="按用户 ID 过滤"
              aria-label="按用户 ID 过滤"
              className="filter-select"
              value={userId}
              onChange={(v) => setUserId(v === null ? undefined : v)}
            />
            <Input.Search
              allowClear
              maxLength={MAX_KEYWORD_LEN}
              placeholder="按名称或地址查找"
              aria-label="按名称或地址查找"
              className="filter-select"
              onSearch={(v) => setKeyword(v.trim())}
            />
          </>
        }
      />
      <ChainExitModal
        exit={editing}
        isOpen={isOpen}
        groups={groups.data ?? []}
        onClose={() => setIsOpen(false)}
        onSaved={() => {
          setIsOpen(false)
          refresh()
        }}
      />
    </>
  )
}

function isPlatformExit(e: AdminChainExit): boolean {
  return e.owner === OWNER_PLATFORM
}

function AssignCell({ exit }: { exit: AdminChainExit }) {
  if (!isPlatformExit(exit)) {
    return <>—</>
  }
  if (exit.assignMode === ASSIGN_USER) {
    if (exit.users.length === 0) {
      return <Typography.Text type="warning">未指定用户，没有用户能用</Typography.Text>
    }
    return (
      <>
        {exit.users.map((u) => (
          <Tag key={u.id} color="blue">
            {u.username}
          </Tag>
        ))}
      </>
    )
  }
  if (exit.nodeGroups.length === 0) {
    return <Typography.Text type="warning">未挂节点组，没有用户能用</Typography.Text>
  }
  return (
    <>
      {exit.nodeGroups.map((g) => (
        <Tag key={g.id}>{g.name}</Tag>
      ))}
    </>
  )
}

function UsageCell({ exit }: { exit: AdminChainExit }) {
  if (!isPlatformExit(exit)) {
    return <>—</>
  }
  return (
    <>
      {exit.allowServer && <Tag color="blue">服务端</Tag>}
      {exit.allowClient && <Tag color="cyan">客户端</Tag>}
    </>
  )
}

interface RowActionsProps {
  exit: AdminChainExit
  /** 延迟测试可选的起点；null = 当前管理员不能测（没有 server:read）。 */
  servers: Server[] | null
  onEdit: () => void
  onToggle: () => Promise<boolean>
  onDelete: () => Promise<boolean>
}

/** 行内操作：平台出口可编辑与查看链接；用户出口只能停用 / 启用与删除（Ruling-50）；两种都可以测试延迟（Ruling-52）。 */
function RowActions({ exit, servers, onEdit, onToggle, onDelete }: RowActionsProps) {
  const affected = exit.routeCount === NO_ROUTES ? '没有规则引用它。' : `会连带删除引用它的 ${exit.routeCount} 条规则。`
  return (
    <Space>
      {isPlatformExit(exit) && (
        <>
          <Button size="small" onClick={onEdit}>
            编辑
          </Button>
          <LinkButton exit={exit} />
        </>
      )}
      {servers !== null && <ChainExitTestButton exit={exit} servers={servers} />}
      <Popconfirm
        title={exit.isEnabled ? `停用出口 ${exit.name}？` : `启用出口 ${exit.name}？`}
        description={exit.isEnabled ? '引用它的规则会阻断，不会被删除。' : '引用它的规则恢复生效。'}
        onConfirm={onToggle}
      >
        <Button size="small">{exit.isEnabled ? '停用' : '启用'}</Button>
      </Popconfirm>
      <Popconfirm title={`删除出口 ${exit.name}？`} description={affected} onConfirm={onDelete}>
        <Button size="small" danger>
          删除
        </Button>
      </Popconfirm>
    </Space>
  )
}

/** 查看平台出口的链接：点了才向后端取一次（记审计），只留在弹窗的内存里，关闭即丢弃。 */
function LinkButton({ exit }: { exit: AdminChainExit }) {
  const { message } = App.useApp()
  const [link, setLink] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const reveal = async () => {
    setIsBusy(true)
    try {
      const res = await api.GET('/api/admin/chain-exits/{id}/link', { params: { path: { id: exit.id } } })
      if (res.error !== undefined || res.data?.data === undefined) {
        void message.error(toApiError(res.error).message)
        return
      }
      setLink(res.data.data.link)
    } catch (err) {
      reportError(err)
      void message.error(toApiError(err).message)
    } finally {
      setIsBusy(false)
    }
  }

  return (
    <>
      <Button size="small" loading={isBusy} onClick={() => void reveal()}>
        查看链接
      </Button>
      <Modal title={`出口链接：${exit.name}`} open={link !== null} onCancel={() => setLink(null)} footer={null} destroyOnHidden>
        <Typography.Paragraph type="secondary">链接含凭据，不要外传。</Typography.Paragraph>
        <Typography.Paragraph copyable className="mono">
          {link ?? ''}
        </Typography.Paragraph>
      </Modal>
    </>
  )
}
