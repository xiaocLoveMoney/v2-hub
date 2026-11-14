import { Alert, Button, Descriptions, Form, Modal, Segmented, Select, Tag, Typography } from 'antd'
import { useState } from 'react'
import { api, csrfHeader, toApiError, type AdminChainExit, type AdminChainExitTestResult, type ApiError, type ChainExitTestMode, type Server } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'

/** 三种测试方式，按从快到慢排（docs/03 §5.21 延迟测试）。 */
const MODE_OPTIONS: { value: ChainExitTestMode; label: string }[] = [
  { value: 'tcp', label: 'TCP 连接' },
  { value: 'http', label: 'HTTP 请求' },
  { value: 'real', label: '真实延迟' },
]
const DEFAULT_MODE: ChainExitTestMode = 'tcp'
const NO_HTTP_STATUS = 0
const SERVER_FIELD_ID = 'chain-exit-test-server'

/** 不通的原因大类的说明（docs/03 §5.21，Ruling-56）；none 与 unknown 不显示这一行。 */
const REASON_TEXT: Record<AdminChainExitTestResult['reason'], string> = {
  none: '',
  unknown: '',
  refused: '出口拒绝连接：端口没有开放，或地址、端口不对',
  timeout: '连接出口超时：地址不通，或出口的防火墙没有放行这台服务器',
  proxy_rejected: '端口能连上，但代理请求被断开：常见是账号或口令不对、协议类型不对、代理不支持 CONNECT',
  proxy_no_response: '端口能连上，但代理请求一直没有回应：常见是这台服务器的 IP 不在代理的白名单里，或代理自己出不了网',
  bad_status: '请求已经过出口发出去，但测试地址返回了异常状态',
}

interface ChainExitTestButtonProps {
  exit: AdminChainExit
  /** 可选的起点：启用中的服务器。 */
  servers: Server[]
}

/**
 * 出口的延迟测试（docs/05 M10 页面，Ruling-52）：选一台服务器与方式，由它的面板去连出口。
 * 结果只留在弹窗的内存里，关闭即丢弃；不通时显示原因大类（Ruling-56）与面板给出的原文。
 */
export function ChainExitTestButton({ exit, servers }: ChainExitTestButtonProps) {
  const [isOpen, setIsOpen] = useState(false)
  const [serverId, setServerId] = useState<number | undefined>(undefined)
  const [mode, setMode] = useState<ChainExitTestMode>(DEFAULT_MODE)
  const [isBusy, setIsBusy] = useState(false)
  const [result, setResult] = useState<AdminChainExitTestResult | null>(null)
  const [error, setError] = useState<ApiError | null>(null)
  const chosen = serverId ?? servers[0]?.id

  const close = () => {
    setIsOpen(false)
    setResult(null)
    setError(null)
  }
  const run = async () => {
    if (chosen === undefined) {
      return
    }
    setIsBusy(true)
    setError(null)
    setResult(null)
    try {
      const res = await api.POST('/api/admin/chain-exits/{id}/test', { params: { ...csrfHeader(), path: { id: exit.id } }, body: { serverId: chosen, mode } })
      if (res.error !== undefined || res.data?.data === undefined) {
        setError(toApiError(res.error))
        return
      }
      setResult(res.data.data)
    } catch (err) {
      reportError(err)
      setError(toApiError(err))
    } finally {
      setIsBusy(false)
    }
  }

  return (
    <>
      <Button size="small" onClick={() => setIsOpen(true)}>
        测试
      </Button>
      <Modal title={`测试出口：${exit.name}`} open={isOpen} onCancel={close} footer={null} destroyOnHidden>
        <Typography.Paragraph type="secondary">由所选服务器的面板去连这个出口，测的是“服务器 → 出口”这一段；结果不保存。</Typography.Paragraph>
        <ErrorAlert error={error} />
        {servers.length === 0 ? (
          <Alert type="info" showIcon title="没有启用中的服务器可作起点" />
        ) : (
          <Form layout="vertical">
            <Form.Item label="起点服务器" htmlFor={SERVER_FIELD_ID}>
              <Select id={SERVER_FIELD_ID} options={servers.map((s) => ({ value: s.id, label: s.name }))} value={chosen} onChange={(v: number) => setServerId(v)} />
            </Form.Item>
            <Form.Item label="方式">
              <Segmented aria-label="方式" options={MODE_OPTIONS} value={mode} onChange={setMode} />
            </Form.Item>
            <Button type="primary" loading={isBusy} onClick={() => void run()}>
              开始测试
            </Button>
          </Form>
        )}
        {result !== null && (
          <Descriptions className="form-hint" size="small" column={1} bordered>
            <Descriptions.Item label="结果">{result.ok ? <Tag color="green">{`${result.delayMs} ms`}</Tag> : <Tag color="red">不通</Tag>}</Descriptions.Item>
            {result.httpStatus !== NO_HTTP_STATUS && <Descriptions.Item label="HTTP 状态">{result.httpStatus}</Descriptions.Item>}
            {result.egressIp !== '' && (
              <Descriptions.Item label="出口 IP">
                <span className="mono">{result.egressRegion === '' ? result.egressIp : `${result.egressIp} (${result.egressRegion})`}</span>
              </Descriptions.Item>
            )}
            {REASON_TEXT[result.reason] !== '' && <Descriptions.Item label="原因">{REASON_TEXT[result.reason]}</Descriptions.Item>}
            {result.error !== '' && <Descriptions.Item label="面板原文">{result.error}</Descriptions.Item>}
          </Descriptions>
        )}
      </Modal>
    </>
  )
}
