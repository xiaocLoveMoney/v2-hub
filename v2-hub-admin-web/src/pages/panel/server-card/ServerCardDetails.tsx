import { Descriptions } from 'antd'
import type { Server } from '../../../api/client'
import { formatTime } from '../../system/format'
import { formatBytes, formatPercent, percentOf } from './server-card-format'

const EMPTY = '—'
/** 内核状态的显示名；其它取值原样显示。 */
const CORE_STATE_LABELS: Record<string, string> = { running: '运行中', stop: '已停止', error: '出错' }

/** 卡片展开区：Swap、负载、连接数、内核与面板版本、入站数、公网地址、系统与证书（docs/05 服务器状态卡片）。 */
export function ServerCardDetails({ server }: { server: Server }) {
  const res = server.resources
  const swap = res === null || res.swapTotal === 0 ? EMPTY : `${formatBytes(res.swapUsed)} / ${formatBytes(res.swapTotal)}（${formatPercent(percentOf(res.swapUsed, res.swapTotal))}）`
  const core = res === null || res.coreState === '' ? EMPTY : `${CORE_STATE_LABELS[res.coreState] ?? res.coreState}${res.coreVersion === '' ? '' : ` · ${res.coreVersion}`}`
  return (
    <Descriptions size="small" column={{ xs: 1, sm: 2 }} className="server-card-details">
      <Descriptions.Item label="Swap">{swap}</Descriptions.Item>
      <Descriptions.Item label="负载 1 / 5 / 15">{res === null ? EMPTY : `${res.load1.toFixed(2)} / ${res.load5.toFixed(2)} / ${res.load15.toFixed(2)}`}</Descriptions.Item>
      <Descriptions.Item label="TCP / UDP 连接">{res === null ? EMPTY : `${res.tcpCount} / ${res.udpCount}`}</Descriptions.Item>
      <Descriptions.Item label="Xray">{core}</Descriptions.Item>
      <Descriptions.Item label="面板">{`${server.providerType} ${server.panelVersion}`}</Descriptions.Item>
      <Descriptions.Item label="入站">{server.inboundCount}</Descriptions.Item>
      <Descriptions.Item label="公网 IPv4">{server.publicIpv4 || EMPTY}</Descriptions.Item>
      <Descriptions.Item label="公网 IPv6">{server.publicIpv6 || EMPTY}</Descriptions.Item>
      <Descriptions.Item label="开机以来收发">{res === null ? EMPTY : `${formatBytes(res.netSent)} / ${formatBytes(res.netRecv)}`}</Descriptions.Item>
      <Descriptions.Item label="流量周期">{`${server.traffic.cycleStart} 起`}</Descriptions.Item>
      <Descriptions.Item label="系统">{server.host === null || server.host.osId === '' ? EMPTY : `${server.host.osId} ${server.host.arch}`}</Descriptions.Item>
      <Descriptions.Item label="面板证书到期">{server.host === null ? EMPTY : formatTime(server.host.certNotAfter)}</Descriptions.Item>
    </Descriptions>
  )
}
