import { Alert, App, Button, Form, Input, InputNumber, Modal, Select } from 'antd'
import { useState } from 'react'
import { api, csrfHeader, type ApiError, type Inbound, type InboundTemplate } from '../../api/client'
import { useOpenGeneration } from '../../hooks/use-open-generation'
import { mutate } from '../../hooks/use-remote'

const MODE_ADVANCED = 'advanced'
const MIN_PORT = 1
const MAX_PORT = 65535

/** 入站模板（与 hub-api.yaml InboundTemplate 一致）；密钥由服务端生成（Ruling-15）。 */
const TEMPLATE_OPTIONS: { value: InboundTemplate | typeof MODE_ADVANCED; label: string }[] = [
  { value: 'vless-reality', label: 'VLESS + Reality（推荐）' },
  { value: 'vmess-ws', label: 'VMess + WebSocket（可配合 CDN Host）' },
  { value: 'shadowsocks-2022', label: 'Shadowsocks 2022' },
  { value: MODE_ADVANCED, label: '高级 JSON（其它协议）' },
]

const SS2022_METHODS = [
  { value: '2022-blake3-aes-256-gcm', label: '2022-blake3-aes-256-gcm' },
  { value: '2022-blake3-aes-128-gcm', label: '2022-blake3-aes-128-gcm' },
]

const ADVANCED_PLACEHOLDER = '{\n  "settings": {},\n  "streamSettings": {},\n  "sniffing": {}\n}'

interface CreateForm {
  mode: string
  remark: string
  port: number
  target?: string
  serverNames?: string[]
  path?: string
  method?: string
  protocol?: string
  configText?: string
}

/** 把文本解析成 JSON 对象；失败返回 null。 */
function parseObject(text: string | undefined): Record<string, unknown> | null {
  if (text === undefined || text.trim() === '') {
    return null
  }
  try {
    const value: unknown = JSON.parse(text)
    return typeof value === 'object' && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : null
  } catch {
    // 解析失败即视为非法输入，由调用方提示用户
    return null
  }
}

const jsonRule = {
  validator: async (_: unknown, value: string | undefined) => {
    if (value === undefined || value.trim() === '' || parseObject(value) !== null) {
      return
    }
    throw new Error('须为 JSON 对象')
  },
}

interface CreateInboundModalProps {
  serverId: number
  isOpen: boolean
  onClose: () => void
  onSaved: () => void
}

/**
 * 在服务器上新建入站的弹窗。表单实例与错误状态放在 CreateInboundForm 里，每次打开都重建，
 * 上一次填的值不会带到下一次打开。
 */
export function CreateInboundModal({ serverId, isOpen, onClose, onSaved }: CreateInboundModalProps) {
  const generation = useOpenGeneration(isOpen)
  return (
    <Modal title="新建入站" open={isOpen} onCancel={onClose} footer={null} destroyOnHidden width={600}>
      <CreateInboundForm key={generation} serverId={serverId} onSaved={onSaved} />
    </Modal>
  )
}

interface CreateInboundFormProps {
  serverId: number
  onSaved: () => void
}

/** 新建入站表单本体：模板或高级 JSON。 */
function CreateInboundForm({ serverId, onSaved }: CreateInboundFormProps) {
  const [form] = Form.useForm<CreateForm>()
  const { message } = App.useApp()
  const [error, setError] = useState<ApiError | null>(null)
  const [isBusy, setIsBusy] = useState(false)
  const mode = Form.useWatch('mode', form)

  const handleFinish = async (v: CreateForm) => {
    setIsBusy(true)
    setError(null)
    const base = { remark: v.remark, port: v.port, enabled: true }
    const body =
      v.mode === MODE_ADVANCED
        ? { ...base, protocol: v.protocol ?? '', config: parseObject(v.configText) ?? {} }
        : {
            ...base,
            template: v.mode as InboundTemplate,
            templateParams: { target: v.target, serverNames: v.serverNames, path: v.path, method: v.method },
          }
    const err = await mutate(() => api.POST('/api/admin/servers/{id}/inbounds', { params: { ...csrfHeader(), path: { id: serverId } }, body }))
    setIsBusy(false)
    if (err !== null) {
      setError(err)
      return
    }
    void message.success('已在面板上创建入站')
    onSaved()
  }

  return (
    <>
      {error !== null && <Alert type="error" title={error.message} showIcon className="page-alert" />}
      <Form<CreateForm> form={form} layout="vertical" onFinish={handleFinish} initialValues={{ mode: 'vless-reality', method: SS2022_METHODS[0].value }}>
        <Form.Item name="mode" label="类型">
          <Select options={TEMPLATE_OPTIONS} />
        </Form.Item>
        <Form.Item name="remark" label="备注" rules={[{ required: true, max: 255, message: '1–255 个字符' }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="port" label="端口" rules={[{ required: true, type: 'integer', min: MIN_PORT, max: MAX_PORT, message: '1–65535' }]}>
          <InputNumber min={MIN_PORT} max={MAX_PORT} className="full-width" />
        </Form.Item>
        {mode === 'vless-reality' && (
          <>
            <Form.Item name="target" label="伪装目标" extra="域名:端口，如 www.cloudflare.com:443" rules={[{ required: true, message: '请输入伪装目标' }]}>
              <Input autoComplete="off" />
            </Form.Item>
            <Form.Item name="serverNames" label="SNI（留空取伪装目标的域名）">
              <Select mode="tags" open={false} />
            </Form.Item>
          </>
        )}
        {mode === 'vmess-ws' && (
          <Form.Item name="path" label="WebSocket 路径" initialValue="/">
            <Input autoComplete="off" />
          </Form.Item>
        )}
        {mode === 'shadowsocks-2022' && (
          <Form.Item name="method" label="加密方式">
            <Select options={SS2022_METHODS} />
          </Form.Item>
        )}
        {mode === MODE_ADVANCED && (
          <>
            <Form.Item name="protocol" label="协议" rules={[{ required: true, pattern: /^[a-z][a-z0-9-]{1,31}$/, message: '如 trojan、hysteria2' }]}>
              <Input autoComplete="off" />
            </Form.Item>
            <Form.Item name="configText" label="配置（settings / streamSettings / sniffing）" rules={[{ required: true, message: '请输入配置' }, jsonRule]}>
              <Input.TextArea rows={10} placeholder={ADVANCED_PLACEHOLDER} className="mono" />
            </Form.Item>
          </>
        )}
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          创建
        </Button>
      </Form>
    </>
  )
}

interface EditForm {
  remark: string
  port: number
  configText?: string
}

interface EditInboundModalProps {
  inbound: Inbound | null
  onClose: () => void
  onSaved: () => void
}

/** 修改入站。配置留空不改；修改配置时省略的敏感字段与现有 clients 由服务端从面板补回（Ruling-14）。 */
export function EditInboundModal({ inbound, onClose, onSaved }: EditInboundModalProps) {
  const { message } = App.useApp()
  const [error, setError] = useState<ApiError | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const handleFinish = async (v: EditForm) => {
    if (inbound === null) {
      return
    }
    setIsBusy(true)
    setError(null)
    const config = parseObject(v.configText)
    const err = await mutate(() =>
      api.PUT('/api/admin/inbounds/{id}', {
        params: { ...csrfHeader(), path: { id: inbound.id } },
        body: { remark: v.remark, port: v.port, listen: inbound.listen, ...(config === null ? {} : { config }) },
      }),
    )
    setIsBusy(false)
    if (err !== null) {
      setError(err)
      return
    }
    void message.success('已保存到面板')
    onSaved()
  }

  return (
    <Modal title={`编辑入站：${inbound?.remark ?? ''}`} open={inbound !== null} onCancel={onClose} footer={null} destroyOnHidden width={640}>
      {error !== null && <Alert type="error" title={error.message} showIcon className="page-alert" />}
      <Form<EditForm>
        layout="vertical"
        onFinish={handleFinish}
        initialValues={{ remark: inbound?.remark ?? '', port: inbound?.port ?? MIN_PORT, configText: '' }}
      >
        <Form.Item name="remark" label="备注" rules={[{ required: true, max: 255, message: '1–255 个字符' }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="port" label="端口" rules={[{ required: true, type: 'integer', min: MIN_PORT, max: MAX_PORT, message: '1–65535' }]}>
          <InputNumber min={MIN_PORT} max={MAX_PORT} className="full-width" />
        </Form.Item>
        <Form.Item
          name="configText"
          label="配置（留空表示不改）"
          extra="以下方当前配置为底修改后粘贴；私钥等敏感字段不展示，省略即保持面板原值"
          rules={[jsonRule]}
        >
          <Input.TextArea rows={8} className="mono" />
        </Form.Item>
        <pre className="config-preview">{JSON.stringify(inbound?.config ?? {}, null, 2)}</pre>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          保存
        </Button>
      </Form>
    </Modal>
  )
}

interface EndpointForm {
  inboundIds: number[]
  addresses: string[]
  port?: number
  remark?: string
}

interface EndpointModalProps {
  serverId: number
  inbounds: Inbound[]
  isOpen: boolean
  onClose: () => void
  onSaved: () => void
}

/** 为入站添加对外地址（3x-ui Host），每个地址生成一个节点。 */
export function EndpointModal({ serverId, inbounds, isOpen, onClose, onSaved }: EndpointModalProps) {
  const { message } = App.useApp()
  const [error, setError] = useState<ApiError | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const handleFinish = async (v: EndpointForm) => {
    setIsBusy(true)
    setError(null)
    const err = await mutate(() =>
      api.POST('/api/admin/servers/{id}/endpoints', {
        params: { ...csrfHeader(), path: { id: serverId } },
        body: { inboundIds: v.inboundIds, addresses: v.addresses, port: v.port ?? 0, remark: v.remark ?? '' },
      }),
    )
    setIsBusy(false)
    if (err !== null) {
      setError(err)
      return
    }
    void message.success('已添加对外地址')
    onSaved()
  }

  return (
    <Modal title="添加对外地址（Host）" open={isOpen} onCancel={onClose} footer={null} destroyOnHidden>
      {error !== null && <Alert type="error" title={error.message} showIcon className="page-alert" />}
      <Form<EndpointForm> layout="vertical" onFinish={handleFinish}>
        <Form.Item name="inboundIds" label="入站" rules={[{ required: true, message: '至少选一个入站' }]}>
          <Select
            mode="multiple"
            options={inbounds.filter((i) => !i.isMissing).map((i) => ({ value: i.id, label: `${i.remark}（${i.protocol}:${i.port}）` }))}
          />
        </Form.Item>
        <Form.Item name="addresses" label="地址" extra="域名或 IP，回车添加多个" rules={[{ required: true, message: '至少填一个地址' }]}>
          <Select mode="tags" open={false} />
        </Form.Item>
        <Form.Item name="port" label="端口（留空沿用入站端口）">
          <InputNumber min={0} max={MAX_PORT} className="full-width" />
        </Form.Item>
        <Form.Item name="remark" label="备注">
          <Input autoComplete="off" />
        </Form.Item>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          添加
        </Button>
      </Form>
    </Modal>
  )
}
