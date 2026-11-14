import { InputNumber, Select, Space, Tag, Typography, type TableProps } from 'antd'
import { useCallback, useState } from 'react'
import { Link } from 'react-router'
import { api, type AdminChainRoute, type AdminChainRouteEntry, type ChainKind, type Server } from '../../api/client'
import { PagedTable } from '../../components/PagedTable'
import { useRemote } from '../../hooks/use-remote'
import { ROUTES } from '../../menu'
import { formatTime } from '../system/format'
import { CHAIN_EXIT_KIND_META, CHAIN_KIND_LABELS, entryStateMeta, routeStateMeta } from './chain-labels'

const loadServers = (signal: AbortSignal) => api.GET('/api/admin/servers', { signal })

const KIND_OPTIONS = (Object.keys(CHAIN_KIND_LABELS) as ChainKind[]).map((k) => ({ value: k, label: `${CHAIN_KIND_LABELS[k]}链式` }))

/** 规则里的一个起飞节点：名称、所在服务器与它自己的状态。 */
function EntryLine({ entry }: { entry: AdminChainRouteEntry }) {
  const meta = entryStateMeta(entry.state, entry.coveredBy)
  return (
    <div>
      {entry.name}（<Link to={ROUTES.SERVER_DETAIL.replace(':id', String(entry.serverId))}>{entry.serverName}</Link>）{' '}
      <Tag color={meta.color}>{meta.label}</Tag>
    </div>
  )
}

/**
 * 全站链式规则（server:read，只读，Ruling-41）：规则由用户自助配置，这里用于排查。
 * 顺序即优先级（Ruling-46）；可按类型、用户与起飞服务器筛选。
 */
export function ChainRoutesPage() {
  const [kind, setKind] = useState<ChainKind | undefined>(undefined)
  const [userId, setUserId] = useState<number | undefined>(undefined)
  const [serverId, setServerId] = useState<number | undefined>(undefined)
  const servers = useRemote<Server[]>(loadServers)
  const load = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) =>
      api.GET('/api/admin/chain-routes', { params: { query: { page, pageSize, kind, userId, serverId } }, signal }),
    [kind, userId, serverId],
  )
  const columns: TableProps<AdminChainRoute>['columns'] = [
    { title: 'ID', dataIndex: 'id' },
    { title: '用户', key: 'user', render: (_: unknown, r: AdminChainRoute) => `${r.user.username} #${r.user.id}` },
    { title: '类型', dataIndex: 'kind', render: (k: ChainKind) => CHAIN_KIND_LABELS[k] },
    { title: '顺序', dataIndex: 'position', render: (p: number) => `第 ${p} 条` },
    {
      title: '起飞节点',
      key: 'entries',
      render: (_: unknown, r: AdminChainRoute) => r.entries.map((e) => <EntryLine key={e.nodeId} entry={e} />),
    },
    {
      title: '落地',
      key: 'exit',
      render: (_: unknown, r: AdminChainRoute) => {
        const meta = CHAIN_EXIT_KIND_META[r.exit.kind]
        return (
          <Space size={4}>
            {r.exit.name}
            <Tag color={meta.color}>{meta.label}</Tag>
          </Space>
        )
      },
    },
    {
      title: '状态',
      key: 'state',
      render: (_: unknown, r: AdminChainRoute) => {
        const meta = routeStateMeta(r)
        return <Tag color={meta.color}>{meta.label}</Tag>
      },
    },
    { title: '创建时间', dataIndex: 'createdAt', render: (v: string) => formatTime(v) },
  ]
  return (
    <>
      <Typography.Title level={3}>链式规则</Typography.Title>
      <Typography.Paragraph type="secondary">
        用户在客户后台自助配置，这里只读。同一用户同一类型的规则越靠前越优先；服务端链式在各面板上的下发状态与重试在对应服务器的详情页。
      </Typography.Paragraph>
      <PagedTable<AdminChainRoute>
        load={load}
        columns={columns}
        filters={
          <>
            <Select
              allowClear
              placeholder="按类型过滤"
              aria-label="按类型过滤"
              className="filter-select"
              options={KIND_OPTIONS}
              value={kind}
              onChange={(v: ChainKind | undefined) => setKind(v)}
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
            <Select
              allowClear
              placeholder="按起飞服务器过滤"
              aria-label="按起飞服务器过滤"
              className="filter-select"
              options={(servers.data ?? []).map((s) => ({ value: s.id, label: s.name }))}
              value={serverId}
              onChange={(v: number | undefined) => setServerId(v)}
            />
          </>
        }
      />
    </>
  )
}
