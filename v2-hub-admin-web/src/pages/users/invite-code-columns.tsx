import { Tag, Typography, type TableProps } from 'antd'
import type { InviteCode } from '../../api/client'
import { formatTime } from '../system/format'

// 邀请码表的通用列：员工的邀请码页与代理的邀请码页共用（docs/03 §5.8、§5.23）

/** 可用次数 0 = 不限。 */
export const UNLIMITED_USES = 0

/**
 * 邀请码表的通用列（码、次数、到期、状态、备注、创建时间）；员工页在码后面插入类型与所属两列，代理页直接用。
 * @returns 新数组，调用方可以再插入或追加列
 */
export function inviteCodeBaseColumns(): NonNullable<TableProps<InviteCode>['columns']> {
  return [
    {
      title: '邀请码',
      dataIndex: 'code',
      render: (code: string) => (
        <Typography.Text copyable className="mono">
          {code}
        </Typography.Text>
      ),
    },
    { title: '已用 / 上限', key: 'uses', render: (_: unknown, c: InviteCode) => `${c.usedCount} / ${c.maxUses === UNLIMITED_USES ? '不限' : c.maxUses}` },
    { title: '到期', dataIndex: 'expiresAt', render: (v: string | null) => (v === null ? '不过期' : formatTime(v)) },
    { title: '状态', dataIndex: 'isDisabled', render: (isDisabled: boolean) => (isDisabled ? <Tag color="red">已停用</Tag> : <Tag color="green">可用</Tag>) },
    { title: '备注', dataIndex: 'remark', render: (v: string) => v || '—' },
    { title: '创建时间', dataIndex: 'createdAt', render: (v: string) => formatTime(v) },
  ]
}
