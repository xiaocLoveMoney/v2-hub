import { Tag, Typography, type TableProps } from 'antd'
import { api, type AuditLog } from '../../api/client'
import { PagedTable } from '../../components/PagedTable'
import { formatDetail, formatTime } from './format'

/** 审计动作的中文名；未登记的动作原样显示。取值见 hub-server service 包的 Action 常量。 */
const ACTION_LABELS: Record<string, string> = {
  'admin.login': '登录',
  'admin.login_failed': '登录失败',
  'admin.logout': '登出',
  'admin.create': '新建管理员',
  'admin.update': '修改管理员',
  'admin.password_reset': '重置密码',
  'role.create': '新建角色',
  'role.update': '修改角色',
  'role.delete': '删除角色',
}

const ACTOR_LABELS: Record<string, string> = { admin: '管理员', user: '用户', system: '系统' }

const loadLogs = (page: number, pageSize: number, signal: AbortSignal) =>
  api.GET('/api/admin/audit-logs', { params: { query: { page, pageSize } }, signal })

/** 审计日志（audit:read），按时间倒序。 */
export function AuditLogsPage() {
  const columns: TableProps<AuditLog>['columns'] = [
    { title: '时间', dataIndex: 'createdAt', width: 180, render: (v: string) => formatTime(v) },
    {
      title: '操作者',
      key: 'actor',
      render: (_: unknown, l: AuditLog) => {
        const actor = ACTOR_LABELS[l.actorType] ?? l.actorType
        return l.actorId === null || l.actorId === undefined ? actor : `${actor} #${l.actorId}`
      },
    },
    {
      title: '动作',
      dataIndex: 'action',
      render: (action: string) => <Tag color={action === 'admin.login_failed' ? 'red' : 'blue'}>{ACTION_LABELS[action] ?? action}</Tag>,
    },
    {
      title: '对象',
      key: 'target',
      render: (_: unknown, l: AuditLog) => (l.targetType === null || l.targetType === undefined ? '—' : `${l.targetType} #${l.targetId ?? ''}`),
    },
    { title: 'IP', dataIndex: 'clientIp', render: (ip: string | null | undefined) => ip ?? '—' },
    {
      title: '详情',
      dataIndex: 'detail',
      render: (detail: unknown) => (
        <Typography.Text code ellipsis={{ tooltip: formatDetail(detail) }} className="audit-detail">
          {formatDetail(detail)}
        </Typography.Text>
      ),
    },
  ]

  return (
    <>
      <Typography.Title level={3}>审计日志</Typography.Title>
      <PagedTable<AuditLog> load={loadLogs} columns={columns} />
    </>
  )
}
