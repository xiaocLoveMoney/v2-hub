import { CloudOutlined, DatabaseOutlined, TeamOutlined } from '@ant-design/icons'
import { Card, Table, Typography, type TableProps } from 'antd'
import { useCallback } from 'react'
import { api, type AgentAllocation, type AgentPlan, type AgentSummary } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { CardPage } from '../../components/CardPage'
import { ErrorAlert } from '../../components/ErrorAlert'
import { PagedTable } from '../../components/PagedTable'
import { StatTile } from '../../components/StatTile'
import { useDateRangeFilter } from '../../hooks/use-date-range-filter'
import { useRemote } from '../../hooks/use-remote'
import { TABLE_SCROLL } from '../system/format'
import { TRAFFIC_RESET_OPTIONS, formatBytes } from '../users/quota'
import { loadAgentPlans, loadAgentSummary } from './agent-api'
import { allocationColumns, poolRemainingOf } from './agent-format'

const UNLIMITED_IP = 0

const planColumns: TableProps<AgentPlan>['columns'] = [
  { title: '套餐', dataIndex: 'name' },
  { title: '流量', dataIndex: 'trafficLimit', render: (v: number) => formatBytes(v) },
  { title: '时长', dataIndex: 'durationDays', render: (v: number) => `${v} 天` },
  { title: '周期重置', dataIndex: 'trafficReset', render: (v: AgentPlan['trafficReset']) => TRAFFIC_RESET_OPTIONS.find((o) => o.value === v)?.label ?? v },
  { title: '同时在线 IP', dataIndex: 'limitIp', render: (v: number) => (v === UNLIMITED_IP ? '不限' : v) },
  { title: '开通一次扣减', dataIndex: 'poolCost', render: (v: number) => <Typography.Text strong>{formatBytes(v)}</Typography.Text> },
]

/** 代理的配额概览（docs/03 §5.23）：流量池、名下用户数、白名单套餐与自己的开通记录。 */
export function AgentSummaryPage() {
  const { session } = useSession()
  const summary = useRemote<AgentSummary>(loadAgentSummary)
  const plans = useRemote<AgentPlan[]>(loadAgentPlans)
  // 开通记录按创建时间过滤
  const { from, to, picker } = useDateRangeFilter()
  const loadAllocations = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) => api.GET('/api/admin/agent/allocations', { params: { query: { page, pageSize, from, to } }, signal }),
    [from, to],
  )
  const s = summary.data
  const greeting = <Typography.Text type="secondary">欢迎，{session?.admin.username}</Typography.Text>

  return (
    <CardPage title="配额概览" extra={greeting}>
      <ErrorAlert error={summary.error} />
      {s !== null && (
        <div className="stat-grid">
          <StatTile
            tone="primary"
            icon={<DatabaseOutlined />}
            label="流量池剩余"
            value={formatBytes(poolRemainingOf(s))}
            foot={`已用 ${formatBytes(s.trafficPoolUsed)} / 总量 ${formatBytes(s.trafficPoolTotal)}`}
          />
          <StatTile
            tone={s.userCount >= s.maxUsers ? 'warning' : 'success'}
            icon={<TeamOutlined />}
            label="名下用户"
            value={`${s.userCount} / ${s.maxUsers}`}
            foot={s.userCount >= s.maxUsers ? '已达上限，不能再新建' : `还可新建 ${s.maxUsers - s.userCount} 个`}
          />
          <StatTile tone="info" icon={<CloudOutlined />} label="可分配套餐" value={plans.data?.length ?? '—'} foot="开通或续期按套餐扣减流量池" />
        </div>
      )}
      <Card title="可分配的套餐" className="page-alert">
        <ErrorAlert error={plans.error} />
        <Table<AgentPlan>
          rowKey="id"
          size="small"
          scroll={TABLE_SCROLL}
          loading={plans.isLoading}
          pagination={false}
          columns={planColumns}
          dataSource={plans.data ?? []}
          locale={{ emptyText: '平台还没有给你分配套餐' }}
        />
        <Typography.Paragraph type="secondary">扣减量 = 套餐流量 × 套餐时长内最多能用到的重置周期数（不重置按 1 期；按小时 = 天数 × 24 + 1，按天 = 天数 + 1，按周 = ⌈天数 / 7⌉ + 1，按月 = ⌈天数 / 28⌉ + 1），以表中“开通一次扣减”为准；删除用户、调低额度、套餐到期都不退回。</Typography.Paragraph>
      </Card>
      <Card title="我的开通记录">
        <PagedTable<AgentAllocation>
          load={loadAllocations}
          columns={allocationColumns()}
          filters={picker}
        />
      </Card>
    </CardPage>
  )
}
