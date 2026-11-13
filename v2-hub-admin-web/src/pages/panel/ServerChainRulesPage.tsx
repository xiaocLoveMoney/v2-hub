import { InputNumber, Select, Tag, Typography, type TableProps } from 'antd'
import { useCallback, useState } from 'react'
import { Link } from 'react-router'
import { api, type AdminServerChainRule, type Server } from '../../api/client'
import { PagedTable } from '../../components/PagedTable'
import { useRemote } from '../../hooks/use-remote'
import { ROUTES } from '../../menu'
import { formatTime } from '../system/format'
import { serverRuleState } from './chain-labels'

const loadServers = (signal: AbortSignal) => api.GET('/api/admin/servers', { signal })

/** 全站服务端链式规则（server:read，只读，Ruling-41）：规则由用户自助配置，这里用于排查；可按用户与入口服务器筛选。 */
export function ServerChainRulesPage() {
  const [userId, setUserId] = useState<number | undefined>(undefined)
  const [serverId, setServerId] = useState<number | undefined>(undefined)
  const servers = useRemote<Server[]>(loadServers)
  const load = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) =>
      api.GET('/api/admin/server-chain-rules', { params: { query: { page, pageSize, userId, serverId } }, signal }),
    [userId, serverId],
  )
  const columns: TableProps<AdminServerChainRule>['columns'] = [
    { title: 'ID', dataIndex: 'id' },
    { title: '用户', key: 'user', render: (_: unknown, r: AdminServerChainRule) => `${r.user.username} #${r.user.id}` },
    {
      title: '入口',
      key: 'entry',
      render: (_: unknown, r: AdminServerChainRule) => (
        <>
          {r.entry.name}（<Link to={ROUTES.SERVER_DETAIL.replace(':id', String(r.entry.serverId))}>{r.entry.serverName}</Link>）
        </>
      ),
    },
    { title: '落地', key: 'exit', render: (_: unknown, r: AdminServerChainRule) => r.exit.name },
    {
      title: '状态',
      key: 'state',
      render: (_: unknown, r: AdminServerChainRule) => {
        const s = serverRuleState(r)
        return <Tag color={s.color}>{s.label}</Tag>
      },
    },
    { title: '创建时间', dataIndex: 'createdAt', render: (v: string) => formatTime(v) },
  ]
  return (
    <>
      <Typography.Title level={3}>服务端链式</Typography.Title>
      <Typography.Paragraph type="secondary">
        用户在客户后台自助配置；入口面板上的下发状态与重试在对应服务器的详情页。
      </Typography.Paragraph>
      <PagedTable<AdminServerChainRule>
        load={load}
        columns={columns}
        filters={
          <>
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
              placeholder="按入口服务器过滤"
              aria-label="按入口服务器过滤"
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
