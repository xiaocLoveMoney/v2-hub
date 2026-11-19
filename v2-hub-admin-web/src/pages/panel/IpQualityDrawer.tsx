import { Button, Descriptions, Drawer, Space, Spin, Table, Typography, type TableProps } from 'antd'
import { useCallback, useState } from 'react'
import { api, type IpCheck, type IpCheckDetail, type IpCheckResult, type PageData } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { IP_CHECK_POLL_INTERVAL_MS, usePollRemote } from '../../hooks/use-polling'
import { useRemote } from '../../hooks/use-remote'
import { TABLE_SCROLL, formatTime } from '../system/format'
import { formatCountry } from './countries'
import { IpQualityCompare } from './IpQualityCompare'
import { IpQualityReport, LabelTag } from './IpQualityReport'
import { EMPTY, isCheckActive, labelOf } from './ip-quality-labels'

/** 服务器的 IP 质量检测历史（docs/03 §5.23 展示）：分页列表，点一行看完整报告，勾选两条成功的记录可对比。 */

const DEFAULT_PAGE_SIZE = 10
const COMPARE_COUNT = 2
const DETAIL_TITLE_LEVEL = 5
const STATUS_SUCCEEDED = 'succeeded'

/** 检测记录的摘要列。 */
const IP_CHECK_COLUMNS: TableProps<IpCheck>['columns'] = [
  { title: '时间', dataIndex: 'createdAt', render: (v: string) => formatTime(v) },
  { title: '来源', dataIndex: 'origin', render: (v: string) => labelOf.origin(v).text },
  { title: '状态', dataIndex: 'status', render: (v: string) => <LabelTag label={labelOf.checkStatus(v)} /> },
  { title: 'IP', dataIndex: 'ip', render: (v: string) => <span className="mono">{v || EMPTY}</span> },
  { title: '使用地', dataIndex: 'country', render: (v: string) => formatCountry(v) },
  { title: 'IP 类型', dataIndex: 'ipKind', render: (v: string) => <LabelTag label={labelOf.ipKind(v)} /> },
  { title: '使用类型', dataIndex: 'usageType', render: (v: string) => <LabelTag label={labelOf.netType(v)} /> },
  { title: '风险', dataIndex: 'riskLevel', render: (v: string) => <LabelTag label={labelOf.risk(v)} /> },
  { title: '黑名单', key: 'dnsbl', render: (_: unknown, c: IpCheck) => (c.dnsblTotal > 0 ? `${c.dnsblListed}/${c.dnsblTotal}` : EMPTY) },
  { title: '流媒体', key: 'media', render: (_: unknown, c: IpCheck) => (c.mediaTotal > 0 ? `${c.mediaOk}/${c.mediaTotal}` : EMPTY) },
  { title: '25 端口', dataIndex: 'port25', render: (v: string) => <LabelTag label={labelOf.port25(v)} /> },
]

interface IpQualityDrawerProps {
  /** 要看历史的服务器；null 时抽屉关闭。 */
  server: { id: number; name: string } | null
  /** 打开时先看的检测；不传则看最新一条。 */
  initialCheckId?: number | null
  onClose: () => void
}

export function IpQualityDrawer({ server, initialCheckId, onClose }: IpQualityDrawerProps) {
  return (
    <Drawer title={server === null ? '' : `IP 质量检测：${server.name}`} open={server !== null} onClose={onClose} size="large" destroyOnHidden>
      {server !== null && <DrawerBody key={`${server.id}:${initialCheckId ?? ''}`} serverId={server.id} initialCheckId={initialCheckId ?? null} />}
    </Drawer>
  )
}

interface DrawerBodyProps {
  serverId: number
  initialCheckId: number | null
}

function DrawerBody({ serverId, initialCheckId }: DrawerBodyProps) {
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(DEFAULT_PAGE_SIZE)
  const load = useCallback(
    (signal: AbortSignal) => api.GET('/api/admin/servers/{id}/ip-checks', { params: { path: { id: serverId }, query: { page, pageSize } }, signal }),
    [serverId, page, pageSize],
  )
  const list = useRemote<PageData<IpCheck>>(load)
  const items = list.data?.items ?? []
  const [viewId, setViewId] = useState<number | null>(initialCheckId)
  const [picked, setPicked] = useState<number[]>([])
  const [compareIds, setCompareIds] = useState<[number, number] | null>(null)
  const currentId = viewId ?? items[0]?.id ?? null

  // 列表里有没结束的检测时跟着刷新
  usePollRemote([list], items.some(isCheckActive), IP_CHECK_POLL_INTERVAL_MS)

  const handleCompare = () => {
    if (picked.length === COMPARE_COUNT) {
      setCompareIds([picked[0], picked[1]])
    }
  }

  return (
    <>
      <ErrorAlert error={list.error} />
      <Space className="page-filters">
        <Button type="primary" disabled={picked.length !== COMPARE_COUNT} onClick={handleCompare}>
          对比所选
        </Button>
        <Typography.Text type="secondary">勾选两条完成的检测可逐项对比；点一行看完整报告</Typography.Text>
      </Space>
      <Table<IpCheck>
        rowKey="id"
        size="small"
        className="page-alert"
        scroll={TABLE_SCROLL}
        loading={list.isLoading && list.data === null}
        columns={IP_CHECK_COLUMNS}
        dataSource={items}
        rowSelection={{
          selectedRowKeys: picked,
          hideSelectAll: true,
          onChange: (keys) => setPicked(keys.map(Number).slice(-COMPARE_COUNT)),
          getCheckboxProps: (c) => ({ disabled: c.status !== STATUS_SUCCEEDED, 'aria-label': `选择 ${formatTime(c.createdAt)} 的检测` }),
        }}
        onRow={(c) => ({
          onClick: () => {
            setCompareIds(null)
            setViewId(c.id)
          },
        })}
        pagination={{
          current: page,
          pageSize,
          total: list.data?.total ?? 0,
          showSizeChanger: true,
          onChange: (p, size) => {
            setPage(p)
            setPageSize(size)
          },
        }}
      />
      {compareIds !== null && <ComparePanel key={compareIds.join(':')} ids={compareIds} />}
      {compareIds === null && currentId !== null && <CheckPanel key={currentId} checkId={currentId} />}
    </>
  )
}

/** 拉一条检测的详情；没结束时定时刷新。 */
function useCheckDetail(checkId: number) {
  const load = useCallback((signal: AbortSignal) => api.GET('/api/admin/ip-checks/{id}', { params: { path: { id: checkId } }, signal }), [checkId])
  const detail = useRemote<IpCheckDetail>(load)
  usePollRemote([detail], isCheckActive(detail.data), IP_CHECK_POLL_INTERVAL_MS)
  return detail
}

/** 契约里 result 可为 null，这里统一成 IpCheckResult | null。 */
function resultOf(d: IpCheckDetail | null): IpCheckResult | null {
  return d?.result ?? null
}

function CheckPanel({ checkId }: { checkId: number }) {
  const detail = useCheckDetail(checkId)
  const d = detail.data
  return (
    <>
      <Typography.Title level={DETAIL_TITLE_LEVEL}>检测报告</Typography.Title>
      <ErrorAlert error={detail.error} />
      {d === null ? (
        <Spin />
      ) : (
        <>
          <Descriptions size="small" column={{ xs: 1, md: 3 }} className="page-alert">
            <Descriptions.Item label="时间">{formatTime(d.createdAt)}</Descriptions.Item>
            <Descriptions.Item label="来源">
              {labelOf.origin(d.origin).text}
              {d.createdByName !== '' && `（${d.createdByName}）`}
            </Descriptions.Item>
            <Descriptions.Item label="服务器侧">{labelOf.hostProbe(d.hostProbe).text}</Descriptions.Item>
          </Descriptions>
          <IpQualityReport check={d} result={resultOf(d)} />
        </>
      )}
    </>
  )
}

function ComparePanel({ ids }: { ids: [number, number] }) {
  const a = useCheckDetail(ids[0])
  const b = useCheckDetail(ids[1])
  return (
    <>
      <Typography.Title level={DETAIL_TITLE_LEVEL}>对比</Typography.Title>
      <ErrorAlert error={a.error ?? b.error} />
      {a.data === null || b.data === null ? <Spin /> : <IpQualityCompare checks={[a.data, b.data]} />}
    </>
  )
}
