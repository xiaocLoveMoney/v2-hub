import { Space, Switch, Table, Tag, Typography, type TableProps } from 'antd'
import { useMemo, useState } from 'react'
import type { IpCheckDetail } from '../../api/client'
import { formatTime } from '../system/format'
import { diffResults, type DiffRow } from './ip-quality-diff'

/** 两次检测的逐项对比（docs/03 §5.23 展示）：两列并排（较早 / 较新），变化的项标出，默认只看变化项。 */

interface IpQualityCompareProps {
  /** 两条检测，顺序不限；按创建时间分出较早与较新。 */
  checks: [IpCheckDetail, IpCheckDetail]
}

export function IpQualityCompare({ checks }: IpQualityCompareProps) {
  const [older, newer] = [...checks].sort((a, b) => a.createdAt.localeCompare(b.createdAt) || a.id - b.id)
  const [isChangedOnly, setIsChangedOnly] = useState(true)
  const rows = useMemo(() => diffResults(older.result, newer.result), [older, newer])
  const changed = rows.filter((r) => r.isChanged).length
  const shown = isChangedOnly ? rows.filter((r) => r.isChanged) : rows
  const columns: TableProps<DiffRow>['columns'] = [
    { title: '分区', dataIndex: 'section', width: 120 },
    { title: '项目', dataIndex: 'label' },
    { title: `较早（${formatTime(older.createdAt)}）`, dataIndex: 'older' },
    {
      title: `较新（${formatTime(newer.createdAt)}）`,
      key: 'newer',
      render: (_: unknown, r: DiffRow) => (
        <Space size={4}>
          {r.newer}
          {r.isChanged && <Tag color="orange">变化</Tag>}
        </Space>
      ),
    },
  ]
  return (
    <>
      <Space className="page-filters">
        <Switch size="small" checked={isChangedOnly} onChange={setIsChangedOnly} aria-label="只看变化项" />
        <Typography.Text>只看变化项</Typography.Text>
        <Typography.Text type="secondary">
          共 {rows.length} 项，{changed} 项有变化
        </Typography.Text>
      </Space>
      <Table<DiffRow>
        rowKey="path"
        size="small"
        pagination={false}
        scroll={{ x: true }}
        rowClassName={(r) => (r.isChanged ? 'ipq-changed' : '')}
        columns={columns}
        dataSource={shown}
        locale={{ emptyText: '两次结果完全相同' }}
      />
    </>
  )
}
