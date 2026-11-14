import { DeleteOutlined, PlusOutlined } from '@ant-design/icons'
import { Alert, App, Button, Card, Form, Radio, Space } from 'antd'
import { useState } from 'react'
import type { ServerWebCert } from '../../../api/client'
import { LinesField, NumberField, SelectField, SwitchField, TagsField, TextField, type Binder } from './fields'
import type { KeyGen } from './protocol-tab'
import { RealityTargetField, type RealityScanApi } from './reality-scan'
import {
  ALPN_VALUES,
  CERT_USAGES,
  REALITY_KEY,
  SECURITY_NONE,
  SECURITY_REALITY,
  SECURITY_TLS,
  SNIFFING_DESTS,
  TLS_KEY,
  TLS_VERSIONS,
  UTLS_FINGERPRINTS,
  canReality,
  canTls,
  defaultCertificate,
  getAt,
  networkOf,
  prefillSecurity,
  randomShortIds,
  securityOf,
  withSecurity,
  withWebCert,
  type JsonObject,
  type Path,
  type Security,
} from './model'

const TLS_FINGERPRINTS = [{ value: '', label: '不指定' }, ...UTLS_FINGERPRINTS.map((f) => ({ value: f, label: f }))]
const TLS_BASE: Path = ['streamSettings', TLS_KEY]
const REALITY_BASE: Path = ['streamSettings', REALITY_KEY]

/** 取面板自己的网页证书路径；失败（已提示）返回 null。 */
export type WebCertFetch = () => Promise<ServerWebCert | null>

const MSG_NO_WEB_CERT = '面板没有配置网页证书，证书路径请手动填写'

function Certificates({ b, fetchWebCert }: { b: Binder; fetchWebCert: WebCertFetch }) {
  const { message } = App.useApp()
  const [loadingIndex, setLoadingIndex] = useState<number | null>(null)
  const path: Path = [...TLS_BASE, 'certificates']
  const certs = (Array.isArray(getAt(b.draft, path)) ? getAt(b.draft, path) : []) as JsonObject[]
  /** 照 3x-ui 的“从面板设置证书”：把面板的网页证书路径填进这一张证书。 */
  const fillFromPanel = async (index: number) => {
    setLoadingIndex(index)
    const files = await fetchWebCert()
    setLoadingIndex(null)
    if (files === null) {
      return
    }
    if (files.certFile === '' || files.keyFile === '') {
      void message.info(MSG_NO_WEB_CERT)
      return
    }
    b.set([...path, index, 'certificateFile'], files.certFile)
    b.set([...path, index, 'keyFile'], files.keyFile)
  }
  return (
    <Form.Item label="证书">
      <Space orientation="vertical" className="full-width">
        {certs.map((c, i) => {
          const base: Path = [...path, i]
          return (
            <Card
              key={i}
              size="small"
              extra={<Button size="small" icon={<DeleteOutlined />} aria-label="删除证书" onClick={() => b.set(path, certs.filter((_, j) => j !== i))} />}
              title={`证书 ${i + 1}`}
            >
              <Form.Item label="来源">
                <Radio.Group
                  value={c.useFile === false ? 'content' : 'file'}
                  onChange={(e) => b.set([...base, 'useFile'], e.target.value === 'file')}
                  options={[
                    { value: 'file', label: '文件路径' },
                    { value: 'content', label: '证书内容' },
                  ]}
                />
              </Form.Item>
              {c.useFile === false ? (
                <>
                  <LinesField b={b} path={[...base, 'certificate']} label="公钥证书" />
                  <LinesField b={b} path={[...base, 'key']} label="私钥" />
                </>
              ) : (
                <>
                  <TextField b={b} path={[...base, 'certificateFile']} label="公钥文件" placeholder="/root/cert/fullchain.pem" />
                  <TextField b={b} path={[...base, 'keyFile']} label="私钥文件" placeholder="/root/cert/privkey.pem" />
                  <Form.Item label=" " colon={false}>
                    <Button size="small" loading={loadingIndex === i} onClick={() => void fillFromPanel(i)}>
                      取面板证书
                    </Button>
                  </Form.Item>
                </>
              )}
              <SelectField b={b} path={[...base, 'usage']} label="用途" options={CERT_USAGES} />
              <SwitchField b={b} path={[...base, 'buildChain']} label="构建证书链" />
              <NumberField b={b} path={[...base, 'ocspStapling']} label="OCSP 装订（秒）" min={0} />
              <SwitchField b={b} path={[...base, 'oneTimeLoading']} label="只加载一次" />
            </Card>
          )
        })}
        <Button size="small" icon={<PlusOutlined />} onClick={() => b.set(path, [...certs, defaultCertificate()])}>
          添加证书
        </Button>
      </Space>
    </Form.Item>
  )
}

function TlsFields({ b, fetchWebCert }: { b: Binder; fetchWebCert: WebCertFetch }) {
  return (
    <>
      <TextField b={b} path={[...TLS_BASE, 'serverName']} label="SNI" />
      <SelectField b={b} path={[...TLS_BASE, 'minVersion']} label="最低版本" options={TLS_VERSIONS} />
      <SelectField b={b} path={[...TLS_BASE, 'maxVersion']} label="最高版本" options={TLS_VERSIONS} />
      <TagsField b={b} path={[...TLS_BASE, 'alpn']} label="ALPN" options={ALPN_VALUES} />
      <SelectField b={b} path={[...TLS_BASE, 'settings', 'fingerprint']} label="uTLS 指纹" options={TLS_FINGERPRINTS} />
      <TextField b={b} path={[...TLS_BASE, 'cipherSuites']} label="加密套件" extra="留空用 Xray 默认" />
      <SwitchField b={b} path={[...TLS_BASE, 'rejectUnknownSni']} label="拒绝未知 SNI" />
      <SwitchField b={b} path={[...TLS_BASE, 'disableSystemRoot']} label="不用系统根证书" />
      <SwitchField b={b} path={[...TLS_BASE, 'enableSessionResumption']} label="会话恢复" />
      <Certificates b={b} fetchWebCert={fetchWebCert} />
      <TextField b={b} path={[...TLS_BASE, 'echServerKeys']} label="ECH 服务端密钥" />
      <TextField b={b} path={[...TLS_BASE, 'settings', 'echConfigList']} label="ECH 配置" extra="写进客户端分享链接" />
    </>
  )
}

function RealityFields({ b, keygen, realityScan }: { b: Binder; keygen: KeyGen; realityScan: RealityScanApi }) {
  const [busy, setBusy] = useState<'' | 'x25519' | 'mldsa65'>('')
  const genX25519 = async () => {
    setBusy('x25519')
    const res = await keygen('x25519')
    setBusy('')
    if (res !== null) {
      b.set([...REALITY_BASE, 'privateKey'], res.privateKey)
      b.set([...REALITY_BASE, 'settings', 'publicKey'], res.publicKey)
    }
  }
  const genMldsa = async () => {
    setBusy('mldsa65')
    const res = await keygen('mldsa65')
    setBusy('')
    if (res !== null) {
      b.set([...REALITY_BASE, 'mldsa65Seed'], res.seed)
      b.set([...REALITY_BASE, 'settings', 'mldsa65Verify'], res.verify)
    }
  }
  return (
    <>
      <SwitchField b={b} path={[...REALITY_BASE, 'show']} label="调试日志" />
      <NumberField b={b} path={[...REALITY_BASE, 'xver']} label="Xver" min={0} />
      <SelectField b={b} path={[...REALITY_BASE, 'settings', 'fingerprint']} label="uTLS 指纹" options={UTLS_FINGERPRINTS} />
      <RealityTargetField b={b} scan={realityScan} />
      <TagsField b={b} path={[...REALITY_BASE, 'serverNames']} label="SNI" extra="回车添加多个，留空则取目标的域名" />
      <TextField b={b} path={[...REALITY_BASE, 'settings', 'serverName']} label="分享链接 SNI" />
      <TextField b={b} path={[...REALITY_BASE, 'privateKey']} label="私钥" />
      <TextField
        b={b}
        path={[...REALITY_BASE, 'settings', 'publicKey']}
        label="公钥"
        action={
          <Button type="primary" loading={busy === 'x25519'} onClick={() => void genX25519()}>
            生成密钥对
          </Button>
        }
      />
      <TagsField
        b={b}
        path={[...REALITY_BASE, 'shortIds']}
        label="Short IDs"
        action={<Button onClick={() => b.set([...REALITY_BASE, 'shortIds'], randomShortIds())}>生成</Button>}
      />
      <TextField b={b} path={[...REALITY_BASE, 'settings', 'spiderX']} label="SpiderX" />
      <NumberField b={b} path={[...REALITY_BASE, 'maxTimediff']} label="最大时间差（ms）" min={0} />
      <TextField b={b} path={[...REALITY_BASE, 'minClientVer']} label="最低客户端版本" />
      <TextField b={b} path={[...REALITY_BASE, 'maxClientVer']} label="最高客户端版本" />
      <TextField b={b} path={[...REALITY_BASE, 'mldsa65Seed']} label="ML-DSA-65 种子" />
      <TextField
        b={b}
        path={[...REALITY_BASE, 'settings', 'mldsa65Verify']}
        label="ML-DSA-65 校验"
        action={
          <Button loading={busy === 'mldsa65'} onClick={() => void genMldsa()}>
            生成
          </Button>
        }
      />
    </>
  )
}

/** 安全页：无 / TLS / Reality，可选项随协议与传输变化（对照 3x-ui）。 */
interface SecurityTabProps {
  b: Binder
  keygen: KeyGen
  fetchWebCert: WebCertFetch
  realityScan: RealityScanApi
}

export function SecurityTab({ b, keygen, fetchWebCert, realityScan }: SecurityTabProps) {
  const { message } = App.useApp()
  const { protocol } = b.draft
  const network = networkOf(b.draft)
  const tlsOk = canTls(protocol, network)
  const realityOk = canReality(protocol, network)
  if (!tlsOk && !realityOk) {
    return <Alert type="info" showIcon title="当前协议与传输不支持 TLS / Reality" />
  }
  const security = securityOf(b.draft)
  const onChange = async (next: Security) => {
    const { hints } = b
    // 新建时把能预判的空项填上（Ruling-53）
    b.update((d) => (hints === null ? withSecurity(d, next) : prefillSecurity(withSecurity(d, next), hints)))
    if (next === SECURITY_TLS && hints !== null) {
      // 新建时顺手取一次面板的网页证书路径，填进还空着的证书
      const files = await fetchWebCert()
      if (files !== null && (files.certFile === '' || files.keyFile === '')) {
        void message.info(MSG_NO_WEB_CERT)
      }
      if (files !== null) {
        b.update((d) => withWebCert(d, files.certFile, files.keyFile).draft)
      }
    }
    if (next === SECURITY_REALITY) {
      // 和 3x-ui 一样，切到 Reality 时直接生成一对密钥
      const res = await keygen('x25519')
      if (res !== null) {
        b.set([...REALITY_BASE, 'privateKey'], res.privateKey)
        b.set([...REALITY_BASE, 'settings', 'publicKey'], res.publicKey)
      }
    }
  }
  const options = [
    { value: SECURITY_NONE, label: '无', disabled: protocol === 'hysteria' },
    { value: SECURITY_TLS, label: 'TLS', disabled: !tlsOk },
    { value: SECURITY_REALITY, label: 'Reality', disabled: !realityOk },
  ]
  return (
    <>
      <Form.Item label="安全">
        <Radio.Group optionType="button" value={security} options={options} onChange={(e) => void onChange(e.target.value as Security)} />
      </Form.Item>
      {security === SECURITY_TLS && <TlsFields b={b} fetchWebCert={fetchWebCert} />}
      {security === SECURITY_REALITY && <RealityFields b={b} keygen={keygen} realityScan={realityScan} />}
    </>
  )
}

/** 嗅探页。 */
export function SniffingTab({ b }: { b: Binder }) {
  return (
    <>
      <SwitchField b={b} path={['sniffing', 'enabled']} label="启用" />
      <TagsField b={b} path={['sniffing', 'destOverride']} label="识别类型" options={SNIFFING_DESTS} />
      <SwitchField b={b} path={['sniffing', 'metadataOnly']} label="只用元数据" />
      <SwitchField b={b} path={['sniffing', 'routeOnly']} label="只用于路由" />
      <TagsField b={b} path={['sniffing', 'ipsExcluded']} label="排除 IP" />
      <TagsField b={b} path={['sniffing', 'domainsExcluded']} label="排除域名" />
    </>
  )
}
