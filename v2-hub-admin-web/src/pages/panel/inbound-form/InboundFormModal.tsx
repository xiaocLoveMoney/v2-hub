import { Alert, App, Button, Form, Input, InputNumber, Modal, Select, Space, Spin, Switch, Tabs } from 'antd'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, csrfHeader, toApiError, type ApiError, type Inbound } from '../../../api/client'
import { useOpenGeneration } from '../../../hooks/use-open-generation'
import { mutate } from '../../../hooks/use-remote'
import type { Binder } from './fields'
import { PROTOCOLS, fromPanelConfig, isKnownProtocol, newDraft, setAt, toConfig, withProtocol, wireguardKeypair, type InboundDraft, type JsonObject, type Path } from './model'
import { ProtocolTab, type KeyGen } from './protocol-tab'
import { SecurityTab, SniffingTab, type WebCertFetch } from './security-tab'
import { TransportTab } from './transport-tab'

const MIN_PORT = 1
const MAX_PORT = 65535
const JSON_INDENT = 2
const LABEL_COL = { flex: '150px' }
const MODAL_WIDTH = 800

interface InboundFormModalProps {
  serverId: number
  /** 服务器的域名，新建时用来预填 Host 与 SNI；不知道或只有 IP 时传空串（Ruling-53）。 */
  serverDomain: string
  /** null 表示新建。 */
  inbound: Inbound | null
  isOpen: boolean
  onClose: () => void
  onSaved: () => void
}

/**
 * 新建 / 编辑入站，表单与 3x-ui 同构（Ruling-16）：基础 / 协议 / 传输 / 安全 / 嗅探 / 高级。
 * 每次打开都重建表单（useOpenGeneration），上一次的草稿不会带到下一次。
 */
export function InboundFormModal({ serverId, serverDomain, inbound, isOpen, onClose, onSaved }: InboundFormModalProps) {
  const generation = useOpenGeneration(isOpen)
  const title = inbound === null ? '新建入站' : `编辑入站：${inbound.remark}`
  return (
    <Modal title={title} open={isOpen} onCancel={onClose} footer={null} destroyOnHidden width={MODAL_WIDTH}>
      <InboundFormBody key={generation} serverId={serverId} serverDomain={serverDomain} inbound={inbound} onClose={onClose} onSaved={onSaved} />
    </Modal>
  )
}

interface BodyProps {
  serverId: number
  serverDomain: string
  inbound: Inbound | null
  onClose: () => void
  onSaved: () => void
}

function parseObject(text: string): JsonObject | null {
  try {
    const v: unknown = JSON.parse(text)
    return v !== null && typeof v === 'object' && !Array.isArray(v) ? (v as JsonObject) : null
  } catch {
    // 解析失败即视为非法输入，由调用方提示
    return null
  }
}

function InboundFormBody({ serverId, serverDomain, inbound, onClose, onSaved }: BodyProps) {
  const { message } = App.useApp()
  const isEdit = inbound !== null
  const [draft, setDraft] = useState<InboundDraft | null>(isEdit ? null : newDraft())
  const [panelProtocol, setPanelProtocol] = useState<string>('')
  const [error, setError] = useState<ApiError | null>(null)
  const [isBusy, setIsBusy] = useState(false)
  const [advancedText, setAdvancedText] = useState<string | null>(null)

  // 编辑：从面板实时读取完整配置（含密钥，Ruling-16）
  useEffect(() => {
    if (inbound === null) {
      return
    }
    const controller = new AbortController()
    const load = async () => {
      try {
        const res = await api.GET('/api/admin/inbounds/{id}/panel-config', { params: { path: { id: inbound.id } }, signal: controller.signal })
        if (controller.signal.aborted) {
          return
        }
        if (res.error !== undefined || res.data?.data === undefined) {
          setError(toApiError(res.error))
          return
        }
        setPanelProtocol(res.data.data.protocol)
        setDraft(fromPanelConfig(res.data.data))
      } catch (err) {
        if (!controller.signal.aborted) {
          reportError(err)
          setError(toApiError(err))
        }
      }
    }
    void load()
    return () => controller.abort()
  }, [inbound])

  const set = useCallback((path: Path, value: unknown) => {
    setDraft((d) => (d === null ? d : setAt(d, path, value)))
    setAdvancedText(null)
  }, [])
  const update = useCallback((change: (d: InboundDraft) => InboundDraft) => {
    setDraft((d) => (d === null ? d : change(d)))
    setAdvancedText(null)
  }, [])

  const keygen: KeyGen = useCallback(
    async (kind) => {
      try {
        const res = await api.POST('/api/admin/servers/{id}/keys', { params: { ...csrfHeader(), path: { id: serverId } }, body: { kind } })
        if (res.error !== undefined || res.data?.data === undefined) {
          void message.error(toApiError(res.error).message)
          return null
        }
        return res.data.data
      } catch (err) {
        reportError(err)
        void message.error(toApiError(err).message)
        return null
      }
    },
    [serverId, message],
  )

  /** 取面板自己的网页证书路径（Ruling-53）；失败时提示并返回 null。 */
  const fetchWebCert: WebCertFetch = useCallback(async () => {
    try {
      const res = await api.GET('/api/admin/servers/{id}/web-cert', { params: { path: { id: serverId } } })
      if (res.error !== undefined || res.data?.data === undefined) {
        void message.error(toApiError(res.error).message)
        return null
      }
      return res.data.data
    } catch (err) {
      reportError(err)
      void message.error(toApiError(err).message)
      return null
    }
  }, [serverId, message])

  // 只在新建时预填；编辑已有入站时照面板上的配置来，不动它
  const hints = useMemo(() => (isEdit ? null : { domain: serverDomain }), [isEdit, serverDomain])

  // 新建 WireGuard 时和 3x-ui 一样自动生成服务端私钥
  const needsWgKey = draft?.protocol === 'wireguard' && draft.settings.secretKey === ''
  useEffect(() => {
    if (!needsWgKey) {
      return
    }
    void wireguardKeypair()
      .then((pair) => set(['settings', 'secretKey'], pair.privateKey))
      .catch((err: unknown) => reportError(err))
  }, [needsWgKey, set])

  if (draft === null) {
    return error === null ? <Spin /> : <Alert type="error" title={error.message} showIcon />
  }
  const b: Binder = { draft, set, update, hints }
  const isFormCovered = !isEdit || isKnownProtocol(panelProtocol)
  const config = toConfig(draft)
  const shownJson = advancedText ?? JSON.stringify(config, null, JSON_INDENT)

  const applyAdvanced = () => {
    const parsed = parseObject(shownJson)
    if (parsed === null) {
      void message.error('须为 JSON 对象')
      return
    }
    const next = fromPanelConfig({ ...draft, config: parsed })
    setDraft({ ...next, protocol: draft.protocol })
    setAdvancedText(null)
    void message.success('已应用到表单')
  }

  const submit = async () => {
    const remark = draft.remark.trim()
    if (remark === '' || !Number.isInteger(draft.port) || draft.port < MIN_PORT || draft.port > MAX_PORT) {
      setError({ code: 'invalid_argument', message: '备注不能为空，端口须为 1–65535' })
      return
    }
    const parsed = advancedText === null ? config : parseObject(advancedText)
    if (parsed === null) {
      setError({ code: 'invalid_argument', message: '高级配置不是合法的 JSON 对象' })
      return
    }
    setIsBusy(true)
    setError(null)
    const err = await mutate(() =>
      inbound === null
        ? api.POST('/api/admin/servers/{id}/inbounds', {
            params: { ...csrfHeader(), path: { id: serverId } },
            body: { remark, port: draft.port, listen: draft.listen, enabled: draft.enabled, protocol: draft.protocol, config: parsed },
          })
        : api.PUT('/api/admin/inbounds/{id}', {
            params: { ...csrfHeader(), path: { id: inbound.id } },
            body: { remark, port: draft.port, listen: draft.listen, config: parsed },
          }),
    )
    setIsBusy(false)
    if (err !== null) {
      setError(err)
      return
    }
    void message.success(isEdit ? '已保存到面板' : '已在面板上创建入站')
    onSaved()
  }

  const basic = (
    <>
      {!isEdit && (
        <Form.Item label="启用" htmlFor="inbound-enabled">
          <Switch id="inbound-enabled" checked={draft.enabled} onChange={(v) => set(['enabled'], v)} />
        </Form.Item>
      )}
      <Form.Item label="备注" required htmlFor="inbound-remark">
        <Input id="inbound-remark" value={draft.remark} autoComplete="off" onChange={(e) => set(['remark'], e.target.value)} />
      </Form.Item>
      <Form.Item label="协议" htmlFor="inbound-protocol">
        <Select<string>
          id="inbound-protocol"
          value={isEdit ? panelProtocol : draft.protocol}
          disabled={isEdit}
          options={PROTOCOLS.map((p) => ({ value: p, label: p }))}
          onChange={(p) => {
            if (isKnownProtocol(p)) {
              update((d) => withProtocol(d, p))
            }
          }}
        />
      </Form.Item>
      <Form.Item label="监听地址" extra="留空表示监听所有 IP" htmlFor="inbound-listen">
        <Input id="inbound-listen" value={draft.listen} autoComplete="off" onChange={(e) => set(['listen'], e.target.value)} />
      </Form.Item>
      <Form.Item label="端口" required htmlFor="inbound-port">
        <InputNumber id="inbound-port" className="full-width" min={MIN_PORT} max={MAX_PORT} value={draft.port} onChange={(v) => set(['port'], typeof v === 'number' ? v : 0)} />
      </Form.Item>
    </>
  )
  const advanced = (
    <>
      <Input.TextArea rows={18} className="mono" value={shownJson} onChange={(e) => setAdvancedText(e.target.value)} />
      <Space className="form-hint">
        <Button onClick={applyAdvanced} disabled={advancedText === null}>
          应用到表单
        </Button>
        <Button onClick={() => setAdvancedText(null)} disabled={advancedText === null}>
          撤销修改
        </Button>
      </Space>
    </>
  )
  const items = isFormCovered
    ? [
        { key: 'basic', label: '基础配置', children: basic },
        { key: 'protocol', label: '协议', children: <ProtocolTab b={b} keygen={keygen} /> },
        { key: 'transport', label: '传输', children: <TransportTab b={b} /> },
        { key: 'security', label: '安全', children: <SecurityTab b={b} keygen={keygen} fetchWebCert={fetchWebCert} /> },
        { key: 'sniffing', label: '嗅探', children: <SniffingTab b={b} /> },
        { key: 'advanced', label: '高级配置', children: advanced },
      ]
    : [
        { key: 'basic', label: '基础配置', children: basic },
        { key: 'advanced', label: '高级配置', children: advanced },
      ]

  return (
    <>
      {error !== null && <Alert type="error" title={error.message} showIcon className="page-alert" />}
      {!isFormCovered && <Alert type="warning" showIcon className="page-alert" title={`表单未覆盖 ${panelProtocol} 协议，请在“高级配置”里编辑`} />}
      {isEdit && <Alert type="info" showIcon className="page-alert" title="以下为面板上的实时配置（含私钥与口令）；入站上的用户由平台维护，保存时保持不变" />}
      <Form layout="horizontal" labelCol={LABEL_COL} labelWrap colon={false}>
        <Tabs items={items} />
      </Form>
      <div className="modal-footer">
        <Space>
          <Button onClick={onClose}>关闭</Button>
          <Button type="primary" loading={isBusy} onClick={() => void submit()}>
            {isEdit ? '保存' : '创建'}
          </Button>
        </Space>
      </div>
    </>
  )
}
