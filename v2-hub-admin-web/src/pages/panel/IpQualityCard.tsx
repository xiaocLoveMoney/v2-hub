import { Button, Descriptions, Space, Typography } from 'antd'
import { useCallback, useState } from 'react'
import { api, csrfHeader, type IpCheck, type PageData, type Server } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { ErrorAlert } from '../../components/ErrorAlert'
import { PageHeader } from '../../components/PageHeader'
import { IP_CHECK_POLL_INTERVAL_MS, usePollRemote } from '../../hooks/use-polling'
import { useRemote } from '../../hooks/use-remote'
import { useRowAction } from '../../hooks/use-submit'
import { PERMISSIONS } from '../../menu'
import { formatTime } from '../system/format'
import { formatCountry } from './countries'
import { IpQualityDrawer } from './IpQualityDrawer'
import { LabelTag } from './IpQualityReport'
import { EMPTY, isCheckActive, labelOf } from './ip-quality-labels'

const CARD_TITLE = 'IP 质量'
const CARD_TITLE_LEVEL = 4
const LATEST_PAGE_SIZE = 1

interface IpQualityCardProps {
  server: Server
}

/**
 * 服务器详情页的 IP 质量卡片（docs/03 §5.23；server:read 才渲染，由调用方判断）：
 * 最近一次检测的摘要、立即检测（server:write；没有 host:write 时只做平台侧）、历史抽屉。检测没结束时每 3 秒刷新。
 */
export function IpQualityCard({ server }: IpQualityCardProps) {
  const { can } = useSession()
  const canStart = can(PERMISSIONS.SERVER_WRITE)
  const canProbeHost = can(PERMISSIONS.HOST_WRITE)
  const [isHistoryOpen, setIsHistoryOpen] = useState(false)
  const [isStarting, setIsStarting] = useState(false)
  const load = useCallback(
    (signal: AbortSignal) =>
      api.GET('/api/admin/servers/{id}/ip-checks', { params: { path: { id: server.id }, query: { page: 1, pageSize: LATEST_PAGE_SIZE } }, signal }),
    [server.id],
  )
  const latest = useRemote<PageData<IpCheck>>(load)
  const last = latest.data?.items[0] ?? null
  const isActive = isCheckActive(last)
  usePollRemote([latest], isActive, IP_CHECK_POLL_INTERVAL_MS)
  const runAction = useRowAction(latest.reload)

  const handleStart = async () => {
    setIsStarting(true)
    await runAction(() => api.POST('/api/admin/servers/{id}/ip-checks', { params: { ...csrfHeader(), path: { id: server.id } } }), '已开始检测')
    setIsStarting(false)
  }

  const hostHint = canProbeHost ? '流媒体与邮局要服务器绑定 SSH' : '没有 host:write，只做平台侧（不检测流媒体与邮局）'
  return (
    <>
      <PageHeader
        title={CARD_TITLE}
        level={CARD_TITLE_LEVEL}
        action={
          <Space>
            {canStart && (
              <Button type="primary" loading={isStarting} disabled={isActive} onClick={() => void handleStart()}>
                {isActive ? '检测中…' : '立即检测'}
              </Button>
            )}
            <Button onClick={() => setIsHistoryOpen(true)} disabled={last === null}>
              历史与对比
            </Button>
          </Space>
        }
      />
      <ErrorAlert error={latest.error} />
      {canStart && (
        <Typography.Paragraph type="secondary">
          检测会把服务器 IP 发给第三方 IP 数据源，并从服务器向流媒体站点与邮局发起连接。{hostHint}。
        </Typography.Paragraph>
      )}
      {last === null && !latest.isLoading && <Typography.Paragraph type="secondary">还没有检测过。</Typography.Paragraph>}
      {last !== null && (
        <Descriptions size="small" column={{ xs: 1, md: 3 }} className="page-alert">
          <Descriptions.Item label="最近检测">
            {formatTime(last.createdAt)} <LabelTag label={labelOf.checkStatus(last.status)} />
          </Descriptions.Item>
          {last.status === 'failed' ? (
            <Descriptions.Item label="原因">{last.lastError || labelOf.error(last.errorCode).text}</Descriptions.Item>
          ) : (
            <>
              <Descriptions.Item label="IP">
                <span className="mono">{last.ip || EMPTY}</span>
                {last.ip !== '' && `（${labelOf.ipSource(last.ipSource).text}）`}
              </Descriptions.Item>
              <Descriptions.Item label="使用地 / 注册地">
                {formatCountry(last.country)} / {formatCountry(last.regCountry)} <LabelTag label={labelOf.ipKind(last.ipKind)} />
              </Descriptions.Item>
              <Descriptions.Item label="ASN">{last.asn === null ? EMPTY : `AS${last.asn} ${last.asOrg}`}</Descriptions.Item>
              <Descriptions.Item label="使用类型">
                <LabelTag label={labelOf.netType(last.usageType)} />
              </Descriptions.Item>
              <Descriptions.Item label="风险">
                <LabelTag label={labelOf.risk(last.riskLevel)} />
              </Descriptions.Item>
              <Descriptions.Item label="黑名单上榜">{last.dnsblTotal > 0 ? `${last.dnsblListed}/${last.dnsblTotal}` : EMPTY}</Descriptions.Item>
              <Descriptions.Item label="流媒体与 AI">
                {last.mediaTotal > 0 ? `${last.mediaOk}/${last.mediaTotal} 可用` : labelOf.hostProbe(last.hostProbe).text}
              </Descriptions.Item>
              <Descriptions.Item label="25 端口">
                <LabelTag label={labelOf.port25(last.port25)} />
              </Descriptions.Item>
            </>
          )}
        </Descriptions>
      )}
      <IpQualityDrawer server={isHistoryOpen ? { id: server.id, name: server.name } : null} onClose={() => setIsHistoryOpen(false)} />
    </>
  )
}
