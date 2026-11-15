import { Button, Descriptions, Modal, Space, Table, Tag, Typography, type TableProps } from 'antd'
import { useCallback, useState } from 'react'
import { api, csrfHeader, toApiError, type ApiError, type NodeReachDetail, type PanelNode, type ReachProbe, type ReachScope, type ReachVerdict } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useRemote } from '../../hooks/use-remote'
import { formatTime } from '../system/format'

/** 大陆连通检测的展示（docs/03 §5.20 大陆连通检测，Ruling-67）：结论标签、节点的检测详情与“立即检测”。 */

const RTT_DECIMALS = 1
const EMPTY = '—'
const MODAL_WIDTH = 720

/** 结论的文字与标签颜色；颜色只是辅助，文字始终在。 */
const VERDICTS: Record<ReachVerdict, { text: string; color?: string; hint: string }> = {
  pending: { text: '未检测', hint: '还没有检测过，等后台轮到或点“立即检测”' },
  ok: { text: '正常', color: 'green', hint: '大陆探测点全部连通' },
  partial: { text: '部分不通', color: 'orange', hint: '大陆探测点有通有不通，可能是个别运营商线路的问题' },
  blocked: { text: '被墙', color: 'red', hint: '大陆探测点全不通，而境外对照点能连上' },
  port_down: { text: '端口不通', color: 'gold', hint: '大陆与境外都连不上：端口没开或被防火墙拦，不是墙的问题' },
  unknown: { text: '未知', hint: '探测点不够、探测服务出错，或地址不是公网地址' },
}

/** 结论的中文名。 */
function reachText(verdict: ReachVerdict): string {
  return VERDICTS[verdict].text
}

interface ReachTagProps {
  verdict: ReachVerdict
  scope: ReachScope
  cnOk: number
  cnTotal: number
}

/** 一个检测结论的标签：结论 + 大陆通过数；只测了 ICMP 的注明“仅 IP”。 */
export function ReachTag({ verdict, scope, cnOk, cnTotal }: ReachTagProps) {
  const v = VERDICTS[verdict]
  return (
    <Tag color={v.color} title={v.hint}>
      {v.text}
      {cnTotal > 0 && ` ${cnOk}/${cnTotal}`}
      {scope === 'ip' && '（仅 IP）'}
    </Tag>
  )
}

const probeColumns: TableProps<ReachProbe>['columns'] = [
  { title: '位置', key: 'place', render: (_: unknown, p: ReachProbe) => `${p.isMainland ? '大陆' : '境外'} · ${p.country} ${p.city}` },
  { title: '网络', dataIndex: 'network' },
  { title: '结果', dataIndex: 'isOk', render: (isOk: boolean) => (isOk ? <Tag color="green">通</Tag> : <Tag color="red">不通</Tag>) },
  { title: '延迟', dataIndex: 'rttMs', align: 'right', render: (v: number | null) => (v === null ? EMPTY : `${v.toFixed(RTT_DECIMALS)} ms`) },
]

function ipLine(d: NodeReachDetail): string {
  if (d.ipReachable === null) {
    return EMPTY
  }
  return d.ipReachable ? '大陆还能 ping 通，只有这个端口不通' : '大陆也 ping 不通，IP 被封'
}

interface DetailBodyProps {
  nodeId: number
  canWrite: boolean
  onChecked: () => void
}

function DetailBody({ nodeId, canWrite, onChecked }: DetailBodyProps) {
  const load = useCallback((signal: AbortSignal) => api.GET('/api/admin/nodes/{id}/reach', { params: { path: { id: nodeId } }, signal }), [nodeId])
  const state = useRemote<NodeReachDetail>(load)
  const [checked, setChecked] = useState<NodeReachDetail | null>(null)
  const [isBusy, setIsBusy] = useState(false)
  const [error, setError] = useState<ApiError | null>(null)
  const d = checked ?? state.data

  const handleCheck = async () => {
    setIsBusy(true)
    setError(null)
    try {
      const res = await api.POST('/api/admin/nodes/{id}/reach/check', { params: { ...csrfHeader(), path: { id: nodeId } } })
      if (res.error !== undefined || res.data?.data === undefined) {
        setError(toApiError(res.error))
        return
      }
      setChecked(res.data.data)
      onChecked()
    } catch (err) {
      reportError(err)
      setError(toApiError(err))
    } finally {
      setIsBusy(false)
    }
  }

  return (
    <>
      <ErrorAlert error={error ?? state.error} />
      {d !== null && (
        <>
          <Descriptions size="small" column={2} bordered>
            <Descriptions.Item label="目标">{`${d.address}:${d.port}（${d.transport.toUpperCase()}）`}</Descriptions.Item>
            <Descriptions.Item label="结论">
              <ReachTag verdict={d.verdict} scope={d.scope} cnOk={d.cnOk} cnTotal={d.cnTotal} />
            </Descriptions.Item>
            <Descriptions.Item label="大陆探测点">{`${d.cnOk} / ${d.cnTotal} 连通`}</Descriptions.Item>
            <Descriptions.Item label="境外对照点">{`${d.controlOk} / ${d.controlTotal} 连通`}</Descriptions.Item>
            <Descriptions.Item label="检测时间">{formatTime(d.checkedAt)}</Descriptions.Item>
            <Descriptions.Item label="被墙起始">{formatTime(d.blockedSince)}</Descriptions.Item>
            <Descriptions.Item label="IP 层" span={2}>
              {ipLine(d)}
            </Descriptions.Item>
          </Descriptions>
          <Typography.Paragraph type="secondary" className="reach-hint">
            {VERDICTS[d.verdict].hint}
            {d.scope === 'ip' && '。这个入站走 UDP，只能测到 IP 层，发现不了只封 UDP 端口的情况'}
            {d.lastError !== '' && `。${d.lastError}`}
          </Typography.Paragraph>
          <Table<ReachProbe>
            size="small"
            rowKey={(p) => `${p.isMainland}-${p.country}-${p.city}-${p.network}`}
            columns={probeColumns}
            dataSource={d.probes}
            pagination={false}
            locale={{ emptyText: '还没有探测点结果' }}
          />
        </>
      )}
      {canWrite && (
        <Space className="reach-actions">
          <Button type="primary" loading={isBusy} onClick={handleCheck}>
            立即检测
          </Button>
          <Typography.Text type="secondary">由第三方探测点发起，最长约半分钟</Typography.Text>
        </Space>
      )}
    </>
  )
}

interface NodeReachButtonProps {
  node: PanelNode
  /** 有 node:write 才能点“立即检测”。 */
  canWrite: boolean
  /** 手动检测完成后调用（刷新列表）。 */
  onChecked: () => void
}

/** 节点列表里的连通结论：点开看各探测点的结果，可立即检测一次。节点不在检测范围时显示占位符。 */
export function NodeReachButton({ node, canWrite, onChecked }: NodeReachButtonProps) {
  const [isOpen, setIsOpen] = useState(false)
  const reach = node.reach
  if (reach === null) {
    return <>{EMPTY}</>
  }
  return (
    <>
      <Button type="text" size="small" className="reach-button" aria-label={`${node.displayName} 的大陆连通：${reachText(reach.verdict)}`} onClick={() => setIsOpen(true)}>
        <ReachTag verdict={reach.verdict} scope={reach.scope} cnOk={reach.cnOk} cnTotal={reach.cnTotal} />
      </Button>
      <Modal title={`大陆连通：${node.displayName}`} open={isOpen} onCancel={() => setIsOpen(false)} footer={null} width={MODAL_WIDTH} destroyOnHidden>
        <DetailBody nodeId={node.id} canWrite={canWrite} onChecked={onChecked} />
      </Modal>
    </>
  )
}
