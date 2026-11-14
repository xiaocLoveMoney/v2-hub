import { PlusOutlined } from '@ant-design/icons'
import { Alert, App, Button, Descriptions, Popconfirm, Space, Switch, Table, Tag, Tooltip, Typography, type TableProps } from 'antd'
import { useCallback, useState } from 'react'
import { Link, useParams } from 'react-router'
import { api, csrfHeader, type Endpoint, type Inbound, type Server } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { mutate, useRemote } from '../../hooks/use-remote'
import { PERMISSIONS, ROUTES } from '../../menu'
import { TABLE_SCROLL, formatTime } from '../system/format'
import { ChainApplyCard } from './ChainApplyCard'
import { EndpointModal } from './InboundModals'
import { InboundFormModal } from './inbound-form/InboundFormModal'
import { serverDomainOf } from './inbound-form/model'
import { ServerStatusTag } from './status'

interface EndpointTagProps {
  endpoint: Endpoint
  inboundRemark: string
  canDelete: boolean
  onDelete: () => Promise<void>
}

/** 一个对外地址；可删时点 × 先二次确认，只删这一个地址（docs/03 §5.1 删除对外地址）。 */
function EndpointTag({ endpoint, inboundRemark, canDelete, onDelete }: EndpointTagProps) {
  const [isConfirming, setIsConfirming] = useState(false)
  const label = endpoint.port > 0 ? `${endpoint.address}:${endpoint.port}` : endpoint.address
  const color = endpoint.isMissing ? 'red' : undefined
  if (!canDelete) {
    return <Tag color={color}>{label}</Tag>
  }
  // Tag 的 × 会阻止事件冒泡，所以由 onClose 显式打开确认框，并阻止 Tag 自行隐藏
  return (
    <Popconfirm
      title={`删除对外地址 ${label}？`}
      description={endpoint.isMissing ? '面板上已不存在，只删除平台记录与节点' : `只删除入站「${inboundRemark}」上的这个地址，其它地址保留`}
      open={isConfirming}
      onOpenChange={setIsConfirming}
      onConfirm={onDelete}
    >
      <Tag
        color={color}
        closable
        onClose={(ev) => {
          ev.preventDefault()
          setIsConfirming(true)
        }}
      >
        {label}
      </Tag>
    </Popconfirm>
  )
}

/** 服务器详情与入站管理（node:read 查看，node:write 增删改；写入一律先写面板，docs/03 §5.1）。 */
export function ServerDetailPage() {
  const params = useParams()
  const serverId = Number(params.id)
  const { can } = useSession()
  const { message } = App.useApp()
  const canWrite = can(PERMISSIONS.NODE_WRITE)
  const [isCreating, setIsCreating] = useState(false)
  const [isAddingEndpoint, setIsAddingEndpoint] = useState(false)
  const [editing, setEditing] = useState<Inbound | null>(null)
  const [busyId, setBusyId] = useState<number | null>(null)

  const loadServer = useCallback((signal: AbortSignal) => api.GET('/api/admin/servers/{id}', { params: { path: { id: serverId } }, signal }), [serverId])
  const loadInbounds = useCallback(
    (signal: AbortSignal) => api.GET('/api/admin/servers/{id}/inbounds', { params: { path: { id: serverId } }, signal }),
    [serverId],
  )
  const server = useRemote<Server>(loadServer)
  const inbounds = useRemote<Inbound[]>(loadInbounds)

  const reloadAll = () => {
    server.reload()
    inbounds.reload()
  }

  const run = async (id: number, action: () => Promise<{ error?: unknown }>, okText: string) => {
    setBusyId(id)
    const err = await mutate(action)
    setBusyId(null)
    if (err !== null) {
      void message.error(err.message)
      return
    }
    void message.success(okText)
    reloadAll()
  }

  const columns: TableProps<Inbound>['columns'] = [
    {
      title: '备注',
      dataIndex: 'remark',
      render: (remark: string, i: Inbound) => (
        <Space>
          {remark}
          {i.isMissing && <Tag color="red">面板上已不存在</Tag>}
          {i.driftDetectedAt !== null && i.driftDetectedAt !== undefined && (
            <Tooltip title={`${formatTime(i.driftDetectedAt)} 发现面板侧改动，平台已按面板现状更新`}>
              <Tag color="orange">面板侧有改动</Tag>
            </Tooltip>
          )}
        </Space>
      ),
    },
    { title: '协议', dataIndex: 'protocol' },
    { title: '端口', dataIndex: 'port' },
    { title: '传输 / 安全', key: 'transport', render: (_: unknown, i: Inbound) => `${i.network || '—'} / ${i.security || 'none'}` },
    {
      title: '对外地址',
      dataIndex: 'endpoints',
      render: (eps: Endpoint[], i: Inbound) =>
        eps.length === 0
          ? '—'
          : eps.map((e) => (
              <EndpointTag
                key={e.id}
                endpoint={e}
                inboundRemark={i.remark}
                canDelete={canWrite}
                onDelete={() => run(e.id, () => api.DELETE('/api/admin/endpoints/{id}', { params: { ...csrfHeader(), path: { id: e.id } } }), '已删除对外地址')}
              />
            )),
    },
    { title: '用户数', dataIndex: 'clientCount' },
    {
      title: '启用',
      dataIndex: 'isEnabled',
      render: (on: boolean, i: Inbound) => (
        <Switch
          checked={on}
          size="small"
          disabled={!canWrite || i.isMissing}
          loading={busyId === i.id}
          onChange={(next) =>
            void run(i.id, () => api.PUT('/api/admin/inbounds/{id}/enabled', { params: { ...csrfHeader(), path: { id: i.id } }, body: { enabled: next } }), next ? '已启用' : '已停用')
          }
        />
      ),
    },
  ]
  if (canWrite) {
    columns.push({
      title: '操作',
      key: 'actions',
      render: (_: unknown, i: Inbound) => (
        <Space>
          <Button size="small" disabled={i.isMissing} onClick={() => setEditing(i)}>
            编辑
          </Button>
          {i.driftDetectedAt !== null && i.driftDetectedAt !== undefined && (
            <Button size="small" onClick={() => void run(i.id, () => api.POST('/api/admin/inbounds/{id}/drift/ack', { params: { ...csrfHeader(), path: { id: i.id } } }), '已确认面板侧改动')}>
              确认改动
            </Button>
          )}
          <Popconfirm
            title={`删除入站 ${i.remark}？`}
            description={i.isMissing ? '面板上已不存在，只删除平台记录与节点' : '会先删除面板上的入站，再删除平台记录与节点'}
            onConfirm={() => run(i.id, () => api.DELETE('/api/admin/inbounds/{id}', { params: { ...csrfHeader(), path: { id: i.id } } }), '已删除')}
          >
            <Button size="small" danger>
              删除
            </Button>
          </Popconfirm>
        </Space>
      ),
    })
  }

  const srv = server.data
  // 新建入站时预填 Host 与 SNI 用的域名：先看已有的对外地址，再看面板地址（docs/03 §5.2 入站预填）
  const serverDomain = serverDomainOf([...(inbounds.data ?? []).flatMap((i) => i.endpoints.map((e) => e.address)), srv?.baseUrl ?? ''])
  return (
    <>
      <Typography.Paragraph>
        <Link to={ROUTES.SERVERS}>← 服务器</Link>
      </Typography.Paragraph>
      {server.error !== null && <Alert type="error" title={server.error.message} showIcon className="page-alert" />}
      {srv !== null && (
        <Descriptions title={srv.name} size="small" column={{ xs: 1, md: 3 }} className="page-alert">
          <Descriptions.Item label="状态">
            <ServerStatusTag status={srv.status} lastError={srv.lastError} isDisabled={srv.isDisabled} />
          </Descriptions.Item>
          <Descriptions.Item label="面板">{srv.providerType} {srv.panelVersion}</Descriptions.Item>
          <Descriptions.Item label="地区">{srv.region || '—'}</Descriptions.Item>
          <Descriptions.Item label="地址">{srv.baseUrl}</Descriptions.Item>
          <Descriptions.Item label="最近同步">{formatTime(srv.lastSyncedAt)}</Descriptions.Item>
          <Descriptions.Item label="备注">{srv.remark || '—'}</Descriptions.Item>
        </Descriptions>
      )}
      {srv !== null && can(PERMISSIONS.SERVER_READ) && <ChainApplyCard serverId={serverId} />}
      <Space className="page-header">
        <Typography.Title level={4}>入站</Typography.Title>
        {canWrite && (
          <Space>
            <Button onClick={() => setIsAddingEndpoint(true)} disabled={(inbounds.data ?? []).length === 0}>
              添加对外地址
            </Button>
            <Button type="primary" icon={<PlusOutlined />} onClick={() => setIsCreating(true)} disabled={srv?.isDisabled ?? true}>
              新建入站
            </Button>
          </Space>
        )}
      </Space>
      {inbounds.error !== null && <Alert type="error" title={inbounds.error.message} showIcon className="page-alert" />}
      <Table<Inbound> rowKey="id" scroll={TABLE_SCROLL} loading={inbounds.isLoading} columns={columns} dataSource={inbounds.data ?? []} pagination={false} />

      <InboundFormModal
        serverId={serverId}
        serverDomain={serverDomain}
        inbound={editing}
        isOpen={isCreating || editing !== null}
        onClose={() => {
          setIsCreating(false)
          setEditing(null)
        }}
        onSaved={() => {
          setIsCreating(false)
          setEditing(null)
          reloadAll()
        }}
      />
      <EndpointModal
        serverId={serverId}
        inbounds={inbounds.data ?? []}
        isOpen={isAddingEndpoint}
        onClose={() => setIsAddingEndpoint(false)}
        onSaved={() => {
          setIsAddingEndpoint(false)
          reloadAll()
        }}
      />
    </>
  )
}
