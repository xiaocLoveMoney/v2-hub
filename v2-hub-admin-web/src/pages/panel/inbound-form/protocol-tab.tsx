import { Alert, App, Button, Form, Input, Select, Space } from 'antd'
import { useEffect, useState } from 'react'
import { NumberField, ObjectListField, SelectField, SwitchField, TextField, KeyValueField, type Binder } from './fields'
import {
  SS_METHODS,
  TRANSPORT_NETWORKS,
  VLESS_AUTHS,
  hasFallbacks,
  randomAlphanum,
  ssServerKey,
  wireguardKeypair,
  wireguardPublicKey,
  type JsonObject,
} from './model'

/** 让面板生成密钥（POST /servers/{id}/keys）；失败时已提示用户并返回 null。 */
export type KeyGen = (kind: 'x25519' | 'mldsa65' | 'vlessenc') => Promise<JsonObject | null>

const CLIENTS_NOTE = '入站上的用户（clients）由平台按套餐下发（M2），这里不填'
const FALLBACK_COLUMNS = [
  { key: 'name', label: 'SNI' },
  { key: 'alpn', label: 'ALPN' },
  { key: 'path', label: '路径' },
  { key: 'dest', label: '目标' },
  { key: 'xver', label: 'xver', type: 'number' as const },
]
const ACCOUNT_COLUMNS = [
  { key: 'user', label: '用户名' },
  { key: 'pass', label: '密码' },
]
const ACCOUNT_USER_LEN = 8
const ACCOUNT_SECRET_LEN = 12
const MIXED_AUTHS = [
  { value: 'password', label: '用户名密码' },
  { value: 'noauth', label: '无需认证' },
]

function newAccount(): JsonObject {
  const pass = randomAlphanum(ACCOUNT_SECRET_LEN)
  return { user: randomAlphanum(ACCOUNT_USER_LEN), pass }
}

function Fallbacks({ b }: { b: Binder }) {
  if (!hasFallbacks(b.draft)) {
    return null
  }
  return <ObjectListField b={b} path={['settings', 'fallbacks']} label="回落" columns={FALLBACK_COLUMNS} newItem={() => ({ name: '', alpn: '', path: '', dest: '', xver: 0 })} />
}

function VlessFields({ b, keygen }: { b: Binder; keygen: KeyGen }) {
  const [auth, setAuth] = useState<string>(VLESS_AUTHS[0].value)
  const [isBusy, setIsBusy] = useState(false)
  const generate = async () => {
    setIsBusy(true)
    const res = await keygen('vlessenc')
    setIsBusy(false)
    const auths = Array.isArray(res?.auths) ? (res.auths as JsonObject[]) : []
    const picked = auths.find((a) => a.id === auth)
    if (picked !== undefined) {
      b.set(['settings', 'decryption'], picked.decryption)
      b.set(['settings', 'encryption'], picked.encryption)
    }
  }
  const clear = () => {
    b.set(['settings', 'decryption'], 'none')
    b.set(['settings', 'encryption'], 'none')
  }
  return (
    <>
      <TextField b={b} path={['settings', 'decryption']} label="解密（decryption）" extra="服务端用；none 表示不启用 VLESS encryption" />
      <TextField b={b} path={['settings', 'encryption']} label="加密（encryption）" extra="写进客户端分享链接" />
      <Form.Item label="生成密钥">
        <Space.Compact>
          <Select value={auth} options={[...VLESS_AUTHS]} onChange={setAuth} popupMatchSelectWidth={false} />
          <Button type="primary" loading={isBusy} onClick={() => void generate()}>
            生成
          </Button>
          <Button onClick={clear}>清除</Button>
        </Space.Compact>
      </Form.Item>
      <Fallbacks b={b} />
    </>
  )
}

function ShadowsocksFields({ b }: { b: Binder }) {
  const method = String(b.draft.settings.method ?? SS_METHODS[0])
  const onMethod = (next: string) => {
    b.set(['settings', 'method'], next)
    b.set(['settings', 'password'], ssServerKey(next))
  }
  return (
    <>
      <SelectField b={b} path={['settings', 'method']} label="加密方式" options={SS_METHODS} onChange={onMethod} />
      <TextField
        b={b}
        path={['settings', 'password']}
        label="服务端密钥"
        extra="2022 系列为指定长度的 Base64 密钥，换加密方式时自动重新生成"
        action={<Button onClick={() => b.set(['settings', 'password'], ssServerKey(method))}>生成</Button>}
      />
      <SelectField b={b} path={['settings', 'network']} label="网络" options={TRANSPORT_NETWORKS} />
      <SwitchField b={b} path={['settings', 'ivCheck']} label="IV 检查" />
    </>
  )
}

function WireguardFields({ b }: { b: Binder }) {
  const { message } = App.useApp()
  const secret = String(b.draft.settings.secretKey ?? '')
  const [publicKey, setPublicKey] = useState('')
  useEffect(() => {
    let isCurrent = true
    void wireguardPublicKey(secret).then((pk) => {
      if (isCurrent) {
        setPublicKey(pk)
      }
    })
    return () => {
      isCurrent = false
    }
  }, [secret])
  const generate = async () => {
    try {
      const pair = await wireguardKeypair()
      b.set(['settings', 'secretKey'], pair.privateKey)
    } catch (err) {
      void message.error('当前浏览器不支持生成 X25519 密钥，请手动填写')
      reportError(err)
    }
  }
  return (
    <>
      <TextField b={b} path={['settings', 'secretKey']} label="私钥" action={<Button onClick={() => void generate()}>生成</Button>} />
      <Form.Item label="公钥" extra="由私钥推出，客户端配置用">
        <Input value={publicKey} readOnly />
      </Form.Item>
      <NumberField b={b} path={['settings', 'mtu']} label="MTU" min={1} />
      <SwitchField b={b} path={['settings', 'noKernelTun']} label="不用内核 TUN" />
      <TextField b={b} path={['settings', 'subnetIp']} label="客户端网段" />
      <NumberField b={b} path={['settings', 'subnetCidr']} label="网段前缀长度" min={1} max={32} />
    </>
  )
}

/** 协议页：各协议自己的 settings。 */
export function ProtocolTab({ b, keygen }: { b: Binder; keygen: KeyGen }) {
  switch (b.draft.protocol) {
    case 'vless':
      return <VlessFields b={b} keygen={keygen} />
    case 'trojan':
      return hasFallbacks(b.draft) ? <Fallbacks b={b} /> : <Alert type="info" showIcon title={`${CLIENTS_NOTE}；传输为 RAW 且启用 TLS / Reality 时可配置回落`} />
    case 'shadowsocks':
      return <ShadowsocksFields b={b} />
    case 'wireguard':
      return <WireguardFields b={b} />
    case 'http':
      return (
        <>
          <ObjectListField b={b} path={['settings', 'accounts']} label="账号" columns={ACCOUNT_COLUMNS} newItem={newAccount} />
          <SwitchField b={b} path={['settings', 'allowTransparent']} label="允许透明代理" />
        </>
      )
    case 'mixed':
      return (
        <>
          <SelectField b={b} path={['settings', 'auth']} label="认证" options={MIXED_AUTHS} />
          {b.draft.settings.auth === 'password' && <ObjectListField b={b} path={['settings', 'accounts']} label="账号" columns={ACCOUNT_COLUMNS} newItem={newAccount} />}
          <SwitchField b={b} path={['settings', 'udp']} label="UDP" />
          <TextField b={b} path={['settings', 'ip']} label="UDP 本地地址" />
        </>
      )
    case 'tunnel':
      return (
        <>
          <TextField b={b} path={['settings', 'rewriteAddress']} label="转发地址" />
          <NumberField b={b} path={['settings', 'rewritePort']} label="转发端口" min={1} max={65535} />
          <SelectField b={b} path={['settings', 'allowedNetwork']} label="网络" options={TRANSPORT_NETWORKS} />
          <SwitchField b={b} path={['settings', 'followRedirect']} label="跟随重定向" />
          <KeyValueField b={b} path={['settings', 'portMap']} label="端口映射" />
        </>
      )
    case 'hysteria':
      return <Alert type="info" showIcon title={`Hysteria 2：${CLIENTS_NOTE}；传输与 TLS 在“传输”“安全”页设置`} />
    case 'vmess':
    default:
      return <Alert type="info" showIcon title={`${CLIENTS_NOTE}；VMess 没有其它入站设置`} />
  }
}
