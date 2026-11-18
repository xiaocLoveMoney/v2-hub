import { Alert, Button, Collapse, Form, Select, Switch } from 'antd'
import { KeyValueField, NumberField, SelectField, SwitchField, TagsField, TextField, type Binder } from './fields'
import {
  NETWORKS,
  NETWORK_HYSTERIA,
  NETWORK_LABELS,
  XHTTP_MODES,
  getAt,
  hasNetworkChoice,
  hasStream,
  networkKey,
  networkOf,
  prefillNetwork,
  randomHex,
  randomPath,
  randomServiceName,
  withNetwork,
  type InboundDraft,
  type Network,
  type Path,
} from './model'

/** 路径输入框旁的“随机”按钮：换一段随机路径。 */
function RandomPathButton({ b, path }: { b: Binder; path: Path }) {
  return <Button onClick={() => b.set(path, randomPath())}>随机</Button>
}

const HEADER_TYPES = [
  { value: 'none', label: '无' },
  { value: 'http', label: 'HTTP 伪装' },
]
const MASQUERADE_TYPES = [
  { value: '', label: '无' },
  { value: 'proxy', label: '反向代理' },
  { value: 'file', label: '静态文件' },
  { value: 'string', label: '固定内容' },
]

/** RAW：可选 HTTP 伪装头。 */
function RawFields({ b, base }: { b: Binder; base: Path }) {
  const header = [...base, 'header']
  const isHttp = getAt(b.draft, [...header, 'type']) === 'http'
  const onType = (t: string) => {
    b.set(header, t === 'http' ? { type: 'http', request: { version: '1.1', method: 'GET', path: ['/'], headers: {} }, response: { version: '1.1', status: '200', reason: 'OK', headers: {} } } : { type: 'none' })
  }
  return (
    <>
      <SwitchField b={b} path={[...base, 'acceptProxyProtocol']} label="接收 PROXY 协议" />
      <SelectField b={b} path={[...header, 'type']} label="伪装类型" options={HEADER_TYPES} onChange={onType} />
      {isHttp && (
        <>
          <TagsField b={b} path={[...header, 'request', 'path']} label="请求路径" />
          <KeyValueField b={b} path={[...header, 'request', 'headers']} label="请求头" valueAsList extra="多个值用逗号分隔" />
          <TextField b={b} path={[...header, 'response', 'status']} label="响应状态码" />
          <TextField b={b} path={[...header, 'response', 'reason']} label="响应原因" />
          <KeyValueField b={b} path={[...header, 'response', 'headers']} label="响应头" valueAsList extra="多个值用逗号分隔" />
        </>
      )}
    </>
  )
}

function XhttpFields({ b, base }: { b: Binder; base: Path }) {
  const more: { key: string; label: string; kind?: 'number' | 'switch' }[] = [
    { key: 'xPaddingObfsMode', label: '填充混淆', kind: 'switch' },
    { key: 'xPaddingKey', label: '填充键' },
    { key: 'xPaddingHeader', label: '填充头' },
    { key: 'xPaddingPlacement', label: '填充位置' },
    { key: 'xPaddingMethod', label: '填充方法' },
    { key: 'sessionIDPlacement', label: '会话 ID 位置' },
    { key: 'sessionIDKey', label: '会话 ID 键' },
    { key: 'sessionIDTable', label: '会话 ID 字符表' },
    { key: 'sessionIDLength', label: '会话 ID 长度' },
    { key: 'seqPlacement', label: '序号位置' },
    { key: 'seqKey', label: '序号键' },
    { key: 'uplinkDataPlacement', label: '上行数据位置' },
    { key: 'uplinkDataKey', label: '上行数据键' },
    { key: 'uplinkHTTPMethod', label: '上行 HTTP 方法' },
    { key: 'scMinPostsIntervalMs', label: '最小 POST 间隔（ms）' },
    { key: 'serverMaxHeaderBytes', label: '最大请求头字节', kind: 'number' },
    { key: 'uplinkChunkSize', label: '上行分块大小', kind: 'number' },
    { key: 'noGRPCHeader', label: '不发 gRPC 头', kind: 'switch' },
  ]
  return (
    <>
      <TextField b={b} path={[...base, 'path']} label="路径" action={<RandomPathButton b={b} path={[...base, 'path']} />} />
      <TextField b={b} path={[...base, 'host']} label="Host" />
      <SelectField b={b} path={[...base, 'mode']} label="模式" options={XHTTP_MODES} />
      <TextField b={b} path={[...base, 'xPaddingBytes']} label="填充字节" />
      <NumberField b={b} path={[...base, 'scMaxBufferedPosts']} label="最多缓冲 POST" min={0} />
      <TextField b={b} path={[...base, 'scMaxEachPostBytes']} label="单个 POST 最大字节" />
      <TextField b={b} path={[...base, 'scStreamUpServerSecs']} label="stream-up 保活（秒）" />
      <SwitchField b={b} path={[...base, 'noSSEHeader']} label="不发 SSE 头" />
      <KeyValueField b={b} path={[...base, 'headers']} label="响应头" />
      <Collapse
        ghost
        items={[
          {
            key: 'more',
            label: '更多 XHTTP 选项',
            children: more.map((f) => {
              const path = [...base, f.key]
              if (f.kind === 'switch') {
                return <SwitchField key={f.key} b={b} path={path} label={f.label} />
              }
              return f.kind === 'number' ? <NumberField key={f.key} b={b} path={path} label={f.label} min={0} /> : <TextField key={f.key} b={b} path={path} label={f.label} />
            }),
          },
        ]}
      />
    </>
  )
}

function NetworkFields({ b, network }: { b: Binder; network: string }) {
  const base: Path = ['streamSettings', networkKey(network)]
  switch (network) {
    case 'kcp':
      return (
        <>
          <NumberField b={b} path={[...base, 'mtu']} label="MTU" min={576} max={1460} />
          <NumberField b={b} path={[...base, 'tti']} label="TTI（ms）" min={10} max={100} />
          <NumberField b={b} path={[...base, 'uplinkCapacity']} label="上行容量（MB/s）" min={0} />
          <NumberField b={b} path={[...base, 'downlinkCapacity']} label="下行容量（MB/s）" min={0} />
          <NumberField b={b} path={[...base, 'cwndMultiplier']} label="拥塞窗口倍数" min={1} />
          <NumberField b={b} path={[...base, 'maxSendingWindow']} label="最大发送窗口" min={0} />
        </>
      )
    case 'ws':
      return (
        <>
          <SwitchField b={b} path={[...base, 'acceptProxyProtocol']} label="接收 PROXY 协议" />
          <TextField b={b} path={[...base, 'path']} label="路径" action={<RandomPathButton b={b} path={[...base, 'path']} />} />
          <TextField b={b} path={[...base, 'host']} label="Host" />
          <KeyValueField b={b} path={[...base, 'headers']} label="请求头" />
          <NumberField b={b} path={[...base, 'heartbeatPeriod']} label="心跳间隔（秒）" min={0} />
        </>
      )
    case 'grpc':
      return (
        <>
          <TextField
            b={b}
            path={[...base, 'serviceName']}
            label="服务名"
            action={<Button onClick={() => b.set([...base, 'serviceName'], randomServiceName())}>随机</Button>}
          />
          <TextField b={b} path={[...base, 'authority']} label="Authority" />
          <SwitchField b={b} path={[...base, 'multiMode']} label="Multi Mode" />
        </>
      )
    case 'httpupgrade':
      return (
        <>
          <SwitchField b={b} path={[...base, 'acceptProxyProtocol']} label="接收 PROXY 协议" />
          <TextField b={b} path={[...base, 'path']} label="路径" action={<RandomPathButton b={b} path={[...base, 'path']} />} />
          <TextField b={b} path={[...base, 'host']} label="Host" />
          <KeyValueField b={b} path={[...base, 'headers']} label="请求头" />
        </>
      )
    case 'xhttp':
      return <XhttpFields b={b} base={base} />
    case 'tcp':
    default:
      return <RawFields b={b} base={base} />
  }
}

/** Salamander 混淆密码的随机字节数（Xray 要求密码至少 4 字节）。 */
const SALAMANDER_PASSWORD_BYTES = 16
const FINALMASK_PATH: Path = ['streamSettings', 'finalmask']
const SALAMANDER_PASSWORD_PATH: Path = [...FINALMASK_PATH, 'udp', 0, 'settings', 'password']

/** 一份只带 Salamander 的 FinalMask（3x-ui v3.11 的 Hysteria2 混淆，docs/03 §5.1 入站表单）。 */
function salamanderMask(password: string) {
  return { udp: [{ type: 'salamander', settings: { password } }] }
}

/** Hysteria2 的 Salamander 混淆：开关与密码都在传输的 FinalMask 里，不是 hysteriaSettings.auth。 */
function SalamanderField({ b }: { b: Binder }) {
  const isOn = getAt(b.draft, [...FINALMASK_PATH, 'udp', 0, 'type']) === 'salamander'
  return (
    <>
      <Form.Item label="Salamander 混淆" extra="打开后客户端要填同样的混淆密码；订阅里会自动带上">
        <Switch
          aria-label="Salamander 混淆"
          checked={isOn}
          onChange={(on) => b.set(FINALMASK_PATH, on ? salamanderMask(randomHex(SALAMANDER_PASSWORD_BYTES)) : undefined)}
        />
      </Form.Item>
      {isOn && (
        <TextField
          b={b}
          path={SALAMANDER_PASSWORD_PATH}
          label="混淆密码"
          extra="至少 4 个字符"
          action={<Button onClick={() => b.set(SALAMANDER_PASSWORD_PATH, randomHex(SALAMANDER_PASSWORD_BYTES))}>随机</Button>}
        />
      )}
    </>
  )
}

function HysteriaFields({ b }: { b: Binder }) {
  const base: Path = ['streamSettings', networkKey(NETWORK_HYSTERIA)]
  const masq: Path = [...base, 'masquerade']
  const type = String(getAt(b.draft, [...masq, 'type']) ?? '')
  return (
    <>
      <SalamanderField b={b} />
      <NumberField b={b} path={[...base, 'udpIdleTimeout']} label="UDP 空闲超时（秒）" min={2} max={600} />
      <SelectField b={b} path={[...masq, 'type']} label="伪装" options={MASQUERADE_TYPES} onChange={(t) => b.set(masq, t === '' ? undefined : { type: t, url: '', dir: '', content: '', rewriteHost: false, insecure: false, headers: {}, statusCode: 0 })} />
      {type === 'proxy' && (
        <>
          <TextField b={b} path={[...masq, 'url']} label="代理地址" />
          <SwitchField b={b} path={[...masq, 'rewriteHost']} label="改写 Host" />
          <SwitchField b={b} path={[...masq, 'insecure']} label="不校验证书" />
        </>
      )}
      {type === 'file' && <TextField b={b} path={[...masq, 'dir']} label="目录" />}
      {type === 'string' && (
        <>
          <TextField b={b} path={[...masq, 'content']} label="内容" />
          <NumberField b={b} path={[...masq, 'statusCode']} label="状态码" min={0} />
        </>
      )}
    </>
  )
}

/** 传输页：选择传输方式并设置对应参数。 */
export function TransportTab({ b }: { b: Binder }) {
  const { protocol } = b.draft
  if (!hasStream(protocol)) {
    return <Alert type="info" showIcon title="该协议没有传输层设置" />
  }
  if (!hasNetworkChoice(protocol)) {
    return <HysteriaFields b={b} />
  }
  const network = networkOf(b.draft)
  return (
    <>
      <Form.Item label="传输">
        <Select<string>
          value={network}
          options={NETWORKS.map((n) => ({ value: n, label: NETWORK_LABELS[n] }))}
          onChange={(n) =>
            b.update((d) => {
              const next: InboundDraft = withNetwork(d, n as Network)
              // 新建时把能预判的空项填上（Ruling-53）
              return b.hints === null ? next : prefillNetwork(next, b.hints)
            })
          }
        />
      </Form.Item>
      <NetworkFields b={b} network={network} />
    </>
  )
}
