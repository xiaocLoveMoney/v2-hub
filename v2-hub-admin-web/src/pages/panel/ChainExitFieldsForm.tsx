import { Checkbox, Form, Input, Segmented, Select, Switch } from 'antd'
import { useId } from 'react'
import {
  EXIT_ALPNS,
  EXIT_FINGERPRINTS,
  EXIT_FLOWS,
  EXIT_NETWORKS,
  EXIT_PROTOCOLS,
  EXIT_SS_METHODS,
  EXIT_VMESS_CIPHERS,
  EXIT_XHTTP_MODES,
  MAX_EXIT_PORT,
  MIN_EXIT_PORT,
  hasFlow,
  hasStream,
  isPlainProxy,
  normalizeExitFields,
  securitiesOf,
  type ExitFieldKey,
  type ExitFields,
  type ExitNetwork,
  type ExitProtocol,
  type ExitSecurity,
} from './chain-exit-link'

const PROTOCOL_LABELS: Record<ExitProtocol, string> = {
  vless: 'VLESS',
  vmess: 'VMess',
  trojan: 'Trojan',
  shadowsocks: 'Shadowsocks',
  socks: 'SOCKS5',
  http: 'HTTP',
  https: 'HTTPS',
}
const NETWORK_LABELS: Record<ExitNetwork, string> = {
  tcp: 'RAW (TCP)',
  ws: 'WebSocket',
  grpc: 'gRPC',
  httpupgrade: 'HTTPUpgrade',
  xhttp: 'XHTTP',
}
const SECURITY_LABELS: Record<ExitSecurity, string> = { none: '无', tls: 'TLS', reality: 'Reality' }
/** 文本字段的长度上限，与后端校验一致（chain_guard.go）。 */
const MAX_HOST_LEN = 255
const MAX_PATH_LEN = 1024
const MAX_CREDENTIAL_LEN = 255
const MAX_SHORT_ID_LEN = 16
const PORT_DIGITS = 5
const PATH_PLACEHOLDER = '/'
const NOT_SET = ''
const NOT_SET_LABEL = '不指定'
const REQUIRED_TEXT = '必填'
const BAD_PORT_TEXT = `${MIN_EXIT_PORT}–${MAX_EXIT_PORT} 的整数`

const optionsOf = (values: readonly string[]) => values.map((v) => ({ value: v, label: v }))
const PROTOCOL_OPTIONS = EXIT_PROTOCOLS.map((p) => ({ value: p, label: PROTOCOL_LABELS[p] }))
const NETWORK_OPTIONS = EXIT_NETWORKS.map((n) => ({ value: n, label: NETWORK_LABELS[n] }))
const FLOW_OPTIONS = [{ value: NOT_SET, label: NOT_SET_LABEL }, ...optionsOf(EXIT_FLOWS)]
const FINGERPRINT_OPTIONS = optionsOf(EXIT_FINGERPRINTS)
const FINGERPRINT_OPTIONS_WITH_NONE = [{ value: NOT_SET, label: NOT_SET_LABEL }, ...FINGERPRINT_OPTIONS]

interface ChainExitFieldsFormProps {
  fields: ExitFields
  onChange: (fields: ExitFields) => void
  /** 缺项或格式不对的字段（exitFieldProblems 的结果）。 */
  problems: ReadonlySet<ExitFieldKey>
  /** 是否把缺项标出来：点过一次提交之后才标。 */
  shouldShowProblems: boolean
}

/**
 * 出口的连接参数表单（docs/05 M9 页面）：字段参照 3x-ui 的出站表单，按协议、传输、安全层显示对应的一组。
 * 受控组件，放在上层的 antd Form 里只借它的排版：这些 Form.Item 不带 name，取值与校验都走 props。
 */
export function ChainExitFieldsForm({ fields, onChange, problems, shouldShowProblems }: ChainExitFieldsFormProps) {
  const baseId = useId()
  const idOf = (key: ExitFieldKey) => `${baseId}-${key}`
  const set = (patch: Partial<ExitFields>) => onChange(normalizeExitFields({ ...fields, ...patch }))
  /** 一行的公共属性：标签对应的控件 id，以及缺项时的报错。 */
  const row = (key: ExitFieldKey, isRequired = false, problemText = REQUIRED_TEXT) => {
    const hasProblem = shouldShowProblems && problems.has(key)
    return {
      htmlFor: idOf(key),
      required: isRequired,
      validateStatus: hasProblem ? ('error' as const) : undefined,
      help: hasProblem ? problemText : undefined,
    }
  }
  const text = (key: 'address' | 'port' | 'id' | 'passphrase' | 'username' | 'host' | 'path' | 'serviceName' | 'sni' | 'publicKey' | 'shortId' | 'spiderX', maxLength: number) => ({
    id: idOf(key),
    autoComplete: 'off',
    spellCheck: false,
    maxLength,
    value: fields[key],
    onChange: (e: { target: { value: string } }) => set({ [key]: e.target.value }),
  })

  const { protocol, network, security } = fields
  const isStream = hasStream(protocol)
  const hasHostPath = isStream && (network === 'ws' || network === 'httpupgrade' || network === 'xhttp')
  const hasTls = isStream && security !== 'none'
  // 链接里带了下拉没有的加密方式时照样显示出来，由后端决定收不收
  const ssMethods = (EXIT_SS_METHODS as readonly string[]).includes(fields.method) ? EXIT_SS_METHODS : [fields.method, ...EXIT_SS_METHODS]

  return (
    <>
      <Form.Item label="协议" {...row('protocol')}>
        <Select id={idOf('protocol')} options={PROTOCOL_OPTIONS} value={protocol} onChange={(v: ExitProtocol) => set({ protocol: v })} />
      </Form.Item>
      <Form.Item label="地址" {...row('address', true)}>
        <Input {...text('address', MAX_HOST_LEN)} />
      </Form.Item>
      <Form.Item label="端口" {...row('port', true, BAD_PORT_TEXT)}>
        <Input {...text('port', PORT_DIGITS)} inputMode="numeric" />
      </Form.Item>

      {(protocol === 'vless' || protocol === 'vmess') && (
        <Form.Item label="ID" {...row('id', true)}>
          <Input {...text('id', MAX_CREDENTIAL_LEN)} placeholder="UUID" />
        </Form.Item>
      )}
      {protocol === 'vmess' && (
        <Form.Item label="加密" {...row('cipher')}>
          <Select id={idOf('cipher')} options={optionsOf(EXIT_VMESS_CIPHERS)} value={fields.cipher} onChange={(v: string) => set({ cipher: v })} />
        </Form.Item>
      )}
      {protocol === 'shadowsocks' && (
        <Form.Item label="加密方式" {...row('method')}>
          <Select id={idOf('method')} options={optionsOf(ssMethods)} value={fields.method} onChange={(v: string) => set({ method: v })} />
        </Form.Item>
      )}
      {isPlainProxy(protocol) && (
        <Form.Item label="用户名" {...row('username')}>
          <Input {...text('username', MAX_CREDENTIAL_LEN)} placeholder="可选" />
        </Form.Item>
      )}
      {(protocol === 'trojan' || protocol === 'shadowsocks' || isPlainProxy(protocol)) && (
        <Form.Item label="密码" {...row('passphrase', !isPlainProxy(protocol))}>
          <Input {...text('passphrase', MAX_CREDENTIAL_LEN)} placeholder={isPlainProxy(protocol) ? '可选' : undefined} />
        </Form.Item>
      )}

      {protocol === 'https' && (
        <>
          <Form.Item label="SNI" {...row('sni')} extra="留空则用地址里的域名">
            <Input {...text('sni', MAX_HOST_LEN)} />
          </Form.Item>
          <Form.Item label="允许不安全" htmlFor={idOf('isInsecure')} extra="跳过证书校验">
            <Switch id={idOf('isInsecure')} checked={fields.isInsecure} onChange={(v) => set({ isInsecure: v })} />
          </Form.Item>
        </>
      )}

      {isStream && (
        <Form.Item label="传输" {...row('network')}>
          <Select id={idOf('network')} options={NETWORK_OPTIONS} value={network} onChange={(v: ExitNetwork) => set({ network: v })} />
        </Form.Item>
      )}
      {hasHostPath && (
        <>
          <Form.Item label="Host" {...row('host')}>
            <Input {...text('host', MAX_HOST_LEN)} />
          </Form.Item>
          <Form.Item label="路径" {...row('path')}>
            <Input {...text('path', MAX_PATH_LEN)} placeholder={PATH_PLACEHOLDER} />
          </Form.Item>
        </>
      )}
      {isStream && network === 'xhttp' && (
        <Form.Item label="模式" {...row('mode')}>
          <Select id={idOf('mode')} options={optionsOf(EXIT_XHTTP_MODES)} value={fields.mode} onChange={(v: string) => set({ mode: v })} />
        </Form.Item>
      )}
      {isStream && network === 'grpc' && (
        <Form.Item label="serviceName" {...row('serviceName')}>
          <Input {...text('serviceName', MAX_HOST_LEN)} />
        </Form.Item>
      )}

      {isStream && (
        <Form.Item label="安全">
          <Segmented
            aria-label="安全"
            options={securitiesOf(protocol).map((s) => ({ value: s, label: SECURITY_LABELS[s] }))}
            value={security}
            onChange={(v) => set({ security: v })}
          />
        </Form.Item>
      )}
      {hasFlow(fields) && (
        <Form.Item label="Flow" {...row('flow')}>
          <Select id={idOf('flow')} options={FLOW_OPTIONS} value={fields.flow} onChange={(v: string) => set({ flow: v })} />
        </Form.Item>
      )}
      {hasTls && (
        <>
          <Form.Item label="SNI" {...row('sni', security === 'reality')}>
            <Input {...text('sni', MAX_HOST_LEN)} />
          </Form.Item>
          <Form.Item label="指纹（uTLS）" {...row('fingerprint')}>
            {/* REALITY 必须有指纹，所以不给“不指定” */}
            <Select
              id={idOf('fingerprint')}
              options={security === 'tls' ? FINGERPRINT_OPTIONS_WITH_NONE : FINGERPRINT_OPTIONS}
              value={fields.fingerprint}
              onChange={(v: string) => set({ fingerprint: v })}
            />
          </Form.Item>
        </>
      )}
      {isStream && security === 'tls' && (
        <>
          <Form.Item label="ALPN">
            {/* 按固定顺序保存，不按勾选先后 */}
            <Checkbox.Group
              options={optionsOf(EXIT_ALPNS)}
              value={fields.alpn}
              onChange={(values: string[]) => set({ alpn: EXIT_ALPNS.filter((a) => values.includes(a)) })}
            />
          </Form.Item>
          {/* vmess 的分享链接带不了这一项 */}
          {protocol !== 'vmess' && (
            <Form.Item label="允许不安全" htmlFor={idOf('isInsecure')} extra="跳过证书校验">
              <Switch id={idOf('isInsecure')} checked={fields.isInsecure} onChange={(v) => set({ isInsecure: v })} />
            </Form.Item>
          )}
        </>
      )}
      {isStream && security === 'reality' && (
        <>
          <Form.Item label="公钥" {...row('publicKey', true)}>
            <Input {...text('publicKey', MAX_CREDENTIAL_LEN)} />
          </Form.Item>
          <Form.Item label="Short ID" {...row('shortId')}>
            <Input {...text('shortId', MAX_SHORT_ID_LEN)} />
          </Form.Item>
          <Form.Item label="SpiderX" {...row('spiderX')}>
            <Input {...text('spiderX', MAX_PATH_LEN)} placeholder={PATH_PLACEHOLDER} />
          </Form.Item>
        </>
      )}
    </>
  )
}
