import { Alert, App, Button, Form, Input, Modal, Radio, Select, Space, Switch, Typography } from 'antd'
import { useState } from 'react'
import { api, csrfHeader, toApiError, type ApiError, type ProviderType, type Server, type TlsMode } from '../../api/client'
import { useOpenGeneration } from '../../hooks/use-open-generation'
import { mutate } from '../../hooks/use-remote'
import { COUNTRY_OPTIONS, countryName } from './countries'

const PROVIDER_OPTIONS: { value: ProviderType; label: string }[] = [{ value: 'xui', label: '3x-ui（v3.x）' }]
const TLS_VERIFY: TlsMode = 'verify'
const TLS_PIN: TlsMode = 'pin'
const TLS_SKIP: TlsMode = 'skip'

interface ServerForm {
  name: string
  providerType: ProviderType
  baseUrl: string
  apiToken?: string
  tlsMode: TlsMode
  certPinSha256?: string
  region?: string
  remark?: string
  isDisabled?: boolean
}

interface ServerFormModalProps {
  /** null 表示新建 */
  server: Server | null
  isOpen: boolean
  onClose: () => void
  onSaved: () => void
}

/**
 * 接入 / 编辑服务器的弹窗。表单实例与错误状态放在 ServerFormBody 里，每次打开都重建，
 * 上一次填的值（含 token）不会带到下一次打开。
 */
export function ServerFormModal({ server, isOpen, onClose, onSaved }: ServerFormModalProps) {
  const generation = useOpenGeneration(isOpen)
  return (
    <Modal title={server === null ? '接入服务器' : `编辑服务器：${server.name}`} open={isOpen} onCancel={onClose} footer={null} destroyOnHidden width={560}>
      <ServerFormBody key={generation} server={server} onSaved={onSaved} />
    </Modal>
  )
}

interface ServerFormBodyProps {
  /** null 表示新建 */
  server: Server | null
  onSaved: () => void
}

/** 服务器表单本体。API token 只写不读：编辑时留空表示沿用原值（docs/03 §5.1）。 */
function ServerFormBody({ server, onSaved }: ServerFormBodyProps) {
  const [form] = Form.useForm<ServerForm>()
  const { message } = App.useApp()
  const [error, setError] = useState<ApiError | null>(null)
  const [isBusy, setIsBusy] = useState(false)
  const tlsMode = Form.useWatch('tlsMode', form)
  const isEdit = server !== null

  const body = (v: ServerForm) => ({
    name: v.name,
    providerType: v.providerType,
    baseUrl: v.baseUrl,
    apiToken: v.apiToken ?? '',
    tlsMode: v.tlsMode,
    certPinSha256: v.tlsMode === TLS_PIN ? (v.certPinSha256 ?? '') : '',
    region: v.region ?? '',
    remark: v.remark ?? '',
    isDisabled: v.isDisabled ?? false,
  })

  const handleTest = async () => {
    const v = await form.validateFields(['providerType', 'baseUrl', 'apiToken', 'tlsMode', 'certPinSha256'])
    setIsBusy(true)
    setError(null)
    try {
      const { data, error: apiError } = await api.POST('/api/admin/servers/test', {
        params: csrfHeader(),
        body: { providerType: v.providerType, baseUrl: v.baseUrl, apiToken: v.apiToken ?? '', tlsMode: v.tlsMode, certPinSha256: v.certPinSha256 },
      })
      if (apiError !== undefined || data?.data === undefined) {
        setError(toApiError(apiError))
        return
      }
      void message.success(`连接成功，面板版本 ${data.data.version}`)
    } catch (err) {
      reportError(err)
      setError(toApiError(err))
    } finally {
      setIsBusy(false)
    }
  }

  const handleFingerprint = async () => {
    const baseUrl = form.getFieldValue('baseUrl') as string | undefined
    if (baseUrl === undefined || baseUrl === '') {
      setError({ code: 'invalid_argument', message: '请先填写面板地址' })
      return
    }
    setIsBusy(true)
    setError(null)
    try {
      const { data, error: apiError } = await api.POST('/api/admin/servers/cert-fingerprint', { params: csrfHeader(), body: { baseUrl } })
      if (apiError !== undefined || data?.data === undefined) {
        setError(toApiError(apiError))
        return
      }
      form.setFieldValue('certPinSha256', data.data.sha256)
    } catch (err) {
      reportError(err)
      setError(toApiError(err))
    } finally {
      setIsBusy(false)
    }
  }

  /** 扫描位置（Ruling-65）：只把结果填进下拉框，保存才生效。 */
  const handleLocate = async () => {
    if (server === null) {
      return
    }
    setIsBusy(true)
    setError(null)
    try {
      const { data, error: apiError } = await api.POST('/api/admin/servers/{id}/locate', { params: { ...csrfHeader(), path: { id: server.id } } })
      if (apiError !== undefined || data?.data === undefined) {
        setError(toApiError(apiError))
        return
      }
      form.setFieldValue('region', data.data.region)
      void message.success(`公网 IP ${data.data.publicIp} 位于${countryName(data.data.region)}，保存后生效`)
    } catch (err) {
      reportError(err)
      setError(toApiError(err))
    } finally {
      setIsBusy(false)
    }
  }

  const handleFinish = async (v: ServerForm) => {
    setIsBusy(true)
    setError(null)
    const err = isEdit
      ? await mutate(() => api.PUT('/api/admin/servers/{id}', { params: { ...csrfHeader(), path: { id: server.id } }, body: body(v) }))
      : await mutate(() => api.POST('/api/admin/servers', { params: csrfHeader(), body: body(v) }))
    setIsBusy(false)
    if (err !== null) {
      setError(err)
      return
    }
    void message.success(isEdit ? '已保存' : '已接入并完成首次同步')
    onSaved()
  }

  return (
    <>
      {error !== null && <Alert type="error" title={error.message} showIcon className="page-alert" />}
      <Form<ServerForm>
        form={form}
        layout="vertical"
        onFinish={handleFinish}
        initialValues={{
          name: server?.name ?? '',
          providerType: server?.providerType ?? 'xui',
          baseUrl: server?.baseUrl ?? '',
          tlsMode: server?.tlsMode ?? TLS_VERIFY,
          certPinSha256: server?.certPinSha256 ?? '',
          region: server?.region === '' ? undefined : server?.region,
          remark: server?.remark ?? '',
          isDisabled: server?.isDisabled ?? false,
        }}
      >
        <Form.Item name="name" label="名称" rules={[{ required: true, max: 64, message: '1–64 个字符' }]}>
          <Input autoComplete="off" placeholder="如 hk-01" />
        </Form.Item>
        <Form.Item name="providerType" label="面板类型" rules={[{ required: true }]}>
          <Select options={PROVIDER_OPTIONS} />
        </Form.Item>
        <Form.Item
          name="baseUrl"
          label="面板地址"
          extra="含端口与 web base path，如 https://panel.example.com:2053/abc"
          rules={[{ required: true, type: 'url', message: '请输入 http(s) 地址' }]}
        >
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item
          name="apiToken"
          label="API token"
          extra={isEdit ? '留空表示沿用原 token' : '在 3x-ui「设置 → 安全 → API Token」中创建，范围选 admin'}
          rules={isEdit ? [] : [{ required: true, message: '请输入 API token' }]}
        >
          <Input.Password autoComplete="new-password" />
        </Form.Item>
        <Form.Item name="tlsMode" label="TLS 校验">
          <Radio.Group
            options={[
              { value: TLS_VERIFY, label: '校验证书' },
              { value: TLS_PIN, label: '固定证书指纹（自签证书）' },
              { value: TLS_SKIP, label: '不校验（仅调试）' },
            ]}
          />
        </Form.Item>
        {tlsMode === TLS_SKIP && <Alert type="warning" title="不校验证书时，token 可能被中间人截获，只用于调试。" showIcon className="page-alert" />}
        {tlsMode === TLS_PIN && (
          <Form.Item label="证书 SHA-256（Base64）" required>
            <Space.Compact className="full-width">
              <Form.Item name="certPinSha256" noStyle rules={[{ required: true, len: 44, message: '44 个字符的 Base64' }]}>
                <Input autoComplete="off" />
              </Form.Item>
              <Button onClick={() => void handleFingerprint()} loading={isBusy}>
                读取
              </Button>
            </Space.Compact>
          </Form.Item>
        )}
        <Form.Item label="位置" extra={isEdit ? '可从列表选，或按服务器公网 IP 扫描' : '留空则接入后按服务器公网 IP 自动扫描'}>
          <Space.Compact className="full-width">
            <Form.Item name="region" noStyle>
              <Select
                aria-label="位置"
                showSearch={{ optionFilterProp: 'label' }}
                allowClear
                placeholder="选择国家或地区"
                options={COUNTRY_OPTIONS as { value: string; label: string }[]}
              />
            </Form.Item>
            {isEdit && (
              <Button onClick={() => void handleLocate()} loading={isBusy}>
                扫描位置
              </Button>
            )}
          </Space.Compact>
        </Form.Item>
        <Form.Item name="remark" label="备注">
          <Input autoComplete="off" />
        </Form.Item>
        {isEdit && (
          <Form.Item name="isDisabled" label="停用（停止探测与同步，不再写入面板）" valuePropName="checked">
            <Switch />
          </Form.Item>
        )}
        <Space>
          <Button onClick={() => void handleTest()} loading={isBusy}>
            测试连接
          </Button>
          <Button type="primary" htmlType="submit" loading={isBusy}>
            {isEdit ? '保存' : '接入'}
          </Button>
        </Space>
        <Typography.Paragraph type="secondary" className="form-hint">
          平台只做控制面，代理流量不经过本服务器。
        </Typography.Paragraph>
      </Form>
    </>
  )
}
