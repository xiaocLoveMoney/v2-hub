import { App, Button, Form, Input, InputNumber, Select, Space, Typography } from 'antd'
import { useState } from 'react'
import { api, csrfHeader, toApiError, type ApiError, type HostDetection, type ServerHostBatchInput } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { FormModal } from '../../components/FormModal'
import { mutate } from '../../hooks/use-remote'
import { MAX_PORT, MIN_PORT } from '../form-limits'
import { DnsAccountSelect } from './DnsAccountSelect'
import { HostDetectionResult } from './HostDetectionResult'
import { DEFAULT_SSH_PORT, MAX_HOST_BATCH, SAME_MACHINE_ADDRESS_HINT, formatHostListErrors, parseHostList } from './host-list'
import { DEFAULT_PROVIDER, PROVIDER_OPTIONS } from './providers'
import { SshCredentialSelect } from './SshCredentialSelect'

const MODAL_WIDTH = 640
const HOST_LIST_ROWS = 8
const HOST_LIST_PLACEHOLDER = '203.0.113.10 hk-01\n203.0.113.11 hk-02\nnode3.example.com'
/** “测试连接”要先校验的字段：凭据、面板类型、端口与清单。 */
const TEST_FIELDS: (keyof HostCreateForm)[] = ['credentialId', 'providerType', 'sshPort', 'hostsText']

interface HostCreateForm {
  credentialId: number
  providerType: ServerHostBatchInput['providerType']
  sshPort: number
  panelPort?: number | null
  dnsAccountId?: number | null
  hostsText: string
}

/** 一次连接测试的结果与被测地址；只留在弹窗的内存里。 */
interface TestOutcome {
  address: string
  detection: HostDetection
}

/** 主机清单的表单校验：逐行检查并指出第几行有问题。 */
async function validateHostList(_: unknown, value: string | undefined): Promise<void> {
  const parsed = parseHostList(value ?? '')
  if (parsed.errors.length > 0) {
    throw new Error(formatHostListErrors(parsed.errors))
  }
  if (parsed.hosts.length === 0) {
    throw new Error('请至少填写一台主机')
  }
}

interface HostCreateModalProps {
  isOpen: boolean
  onClose: () => void
  /** 新建成功后调用（关闭弹窗、刷新列表）。 */
  onSaved: () => void
}

/** 新建主机的弹窗（host:write）：一份凭据、端口与面板类型配一张主机清单，保存后每台各排一条部署任务（docs/03 §5.22 批量）。 */
export function HostCreateModal({ isOpen, onClose, onSaved }: HostCreateModalProps) {
  return (
    <FormModal title="新建主机" isOpen={isOpen} onClose={onClose} width={MODAL_WIDTH}>
      {(generation) => <HostCreateBody key={generation} onSaved={onSaved} />}
    </FormModal>
  )
}

function HostCreateBody({ onSaved }: { onSaved: () => void }) {
  const [form] = Form.useForm<HostCreateForm>()
  const { message } = App.useApp()
  const [error, setError] = useState<ApiError | null>(null)
  const [isBusy, setIsBusy] = useState(false)
  const [outcome, setOutcome] = useState<TestOutcome | null>(null)

  /** 测试连接：用清单第一行连一次并检测，不落库（docs/03 §5.22 连接测试）。 */
  const handleTest = async () => {
    let v: HostCreateForm
    try {
      v = await form.validateFields(TEST_FIELDS)
    } catch {
      // 校验没过：antd 已在对应字段下显示原因，这里不用再提示
      return
    }
    const first = parseHostList(v.hostsText).hosts[0]
    if (first === undefined) {
      return
    }
    setIsBusy(true)
    setError(null)
    setOutcome(null)
    try {
      const { data, error: apiError } = await api.POST('/api/admin/server-hosts/test', {
        params: csrfHeader(),
        body: { address: first.address, sshPort: v.sshPort, credentialId: v.credentialId, providerType: v.providerType },
      })
      if (apiError !== undefined || data?.data === undefined) {
        setError(toApiError(apiError))
        return
      }
      setOutcome({ address: first.address, detection: data.data })
    } catch (err) {
      reportError(err)
      setError(toApiError(err))
    } finally {
      setIsBusy(false)
    }
  }

  const handleFinish = async (v: HostCreateForm) => {
    const hosts = parseHostList(v.hostsText).hosts.map((h) => ({ address: h.address, name: h.name }))
    setIsBusy(true)
    setError(null)
    const err = await mutate(() =>
      api.POST('/api/admin/server-hosts', {
        params: csrfHeader(),
        body: {
          credentialId: v.credentialId,
          sshPort: v.sshPort,
          providerType: v.providerType,
          panelPort: v.panelPort ?? null,
          dnsAccountId: v.dnsAccountId ?? null,
          hosts,
        },
      }),
    )
    setIsBusy(false)
    if (err !== null) {
      setError(err)
      return
    }
    void message.success(`已新建 ${hosts.length} 台主机，部署任务已排队`)
    onSaved()
  }

  return (
    <>
      <ErrorAlert error={error} />
      <Form<HostCreateForm>
        form={form}
        layout="vertical"
        onFinish={handleFinish}
        initialValues={{ providerType: DEFAULT_PROVIDER, sshPort: DEFAULT_SSH_PORT, hostsText: '' }}
      >
        <Form.Item name="credentialId" label="SSH 凭据" extra="登录用户须是 root 或能免密 sudo" rules={[{ required: true, message: '请选择 SSH 凭据' }]}>
          <SshCredentialSelect />
        </Form.Item>
        <Form.Item name="providerType" label="面板类型" rules={[{ required: true }]}>
          <Select options={PROVIDER_OPTIONS} />
        </Form.Item>
        <Form.Item name="sshPort" label="SSH 端口" rules={[{ required: true, message: '请输入 SSH 端口' }]}>
          <InputNumber min={MIN_PORT} max={MAX_PORT} precision={0} className="full-width" />
        </Form.Item>
        <Form.Item name="panelPort" label="指定面板端口" extra="安装面板时用的端口，整批相同；留空则每台各自随机。接管已有面板时不改它的端口">
          <InputNumber min={MIN_PORT} max={MAX_PORT} precision={0} className="full-width" placeholder="留空 = 随机" />
        </Form.Item>
        <Form.Item
          name="dnsAccountId"
          label="DNS 账号"
          extra="选了就给每台主机申请公开可信的证书（Let’s Encrypt）：域名自动取“名称.根域名”，名称转不成域名时取地址，之后可在编辑主机时改。不选则面板用自签证书"
        >
          <DnsAccountSelect />
        </Form.Item>
        <Form.Item
          name="hostsText"
          label="主机清单"
          extra={`每行一台：“地址 名称”，用空白分隔；不写名称时用地址当名称（名称在接入时用作服务器名）。一次最多 ${MAX_HOST_BATCH} 台，任一行不合法则整批都不保存。${SAME_MACHINE_ADDRESS_HINT}`}
          rules={[{ validator: validateHostList }]}
          validateTrigger="onBlur"
        >
          <Input.TextArea rows={HOST_LIST_ROWS} className="mono" autoComplete="off" spellCheck={false} placeholder={HOST_LIST_PLACEHOLDER} />
        </Form.Item>
        <Space>
          <Button onClick={() => void handleTest()} loading={isBusy}>
            测试连接
          </Button>
          <Button type="primary" htmlType="submit" loading={isBusy}>
            新建并部署
          </Button>
        </Space>
        <Typography.Paragraph type="secondary" className="form-hint">
          “测试连接”只测清单的第一行，结果不保存。保存后平台经 SSH 在每台主机上安装面板（已有标准安装的面板则接管），并自动接入为服务器。
        </Typography.Paragraph>
      </Form>
      {outcome !== null && <HostDetectionResult address={outcome.address} detection={outcome.detection} />}
    </>
  )
}
