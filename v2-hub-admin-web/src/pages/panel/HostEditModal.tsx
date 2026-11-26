import { Alert, Button, Form, Input, InputNumber, Select } from 'antd'
import { api, csrfHeader, type ServerHost } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { FormModal } from '../../components/FormModal'
import { useSubmit } from '../../hooks/use-submit'
import { MAX_NAME_LEN, MAX_PORT, MIN_PORT } from '../form-limits'
import { COUNTRY_OPTIONS } from './countries'
import { CredentialSecretField } from './CredentialSecretField'
import { useCredentialSecret } from './credential-secret'
import { DnsAccountSelect } from './DnsAccountSelect'
import { MAX_HOST_ADDRESS_LEN, SAME_MACHINE_ADDRESS_HINT, validateHostAddressField } from './host-list'
import { SshCredentialSelect } from './SshCredentialSelect'

interface HostEditForm {
  name: string
  address: string
  sshPort: number
  credentialId: number
  region?: string
  panelPort?: number | null
  dnsAccountId?: number | null
  domain?: string
  /** 凭据是密码类型且（凭据，地址，端口）有变化时再次输入的密码（Ruling-97）；提交后清空。 */
  credentialSecret?: string
}

/** 域名的最大长度（hub-api.yaml HostDomain）。 */
const MAX_DOMAIN_LEN = 255

interface HostEditModalProps {
  /** 要编辑的主机；null 时弹窗关闭。 */
  host: ServerHost | null
  onClose: () => void
  /** 保存成功后调用（关闭弹窗、刷新列表）。 */
  onSaved: () => void
}

/** 编辑单台主机的弹窗（host:write）；有未结束的任务时后端返回 409（docs/03 §5.22 主机）。 */
export function HostEditModal({ host, onClose, onSaved }: HostEditModalProps) {
  return (
    <FormModal title={host === null ? '' : `编辑主机：${host.name}`} isOpen={host !== null} onClose={onClose}>
      {(generation) => host !== null && <HostEditBody key={generation} host={host} onSaved={onSaved} />}
    </FormModal>
  )
}

function HostEditBody({ host, onSaved }: { host: ServerHost; onSaved: () => void }) {
  const [form] = Form.useForm<HostEditForm>()
  const submit = useSubmit('已保存', onSaved)
  const dnsAccountId = Form.useWatch('dnsAccountId', form) ?? null
  const hasDnsAccount = dnsAccountId !== null
  // Ruling-97：密码凭据改后的（凭据，地址，端口）与原来不同才要再输一次密码
  const secret = useCredentialSecret(form, host)

  const handleFinish = async (v: HostEditForm) => {
    secret.clear()
    await submit.run(() =>
      api.PUT('/api/admin/server-hosts/{id}', {
        params: { ...csrfHeader(), path: { id: host.id } },
        body: {
          name: v.name.trim(),
          address: v.address.trim(),
          sshPort: v.sshPort,
          credentialId: v.credentialId,
          region: v.region ?? '',
          panelPort: v.panelPort ?? null,
          dnsAccountId: v.dnsAccountId ?? null,
          // 没选 DNS 账号时域名必须为空（docs/03 §5.22 域名）
          domain: (v.dnsAccountId ?? null) === null ? '' : (v.domain ?? '').trim(),
          ...secret.bodyPart(v.credentialSecret),
        },
      }),
    )
  }

  return (
    <>
      <ErrorAlert error={submit.error} />
      <Alert type="warning" showIcon className="page-alert" title="改地址或端口会清空已固定的主机密钥，下次连接时重新记录" />
      <Form<HostEditForm>
        form={form}
        layout="vertical"
        onFinish={handleFinish}
        initialValues={{
          name: host.name,
          address: host.address,
          sshPort: host.sshPort,
          credentialId: host.credentialId,
          region: host.region === '' ? undefined : host.region,
          panelPort: host.panelPort,
          dnsAccountId: host.dnsAccountId,
          domain: host.domain,
        }}
      >
        <Form.Item name="name" label="名称" extra="接入时用作服务器名" rules={[{ required: true, whitespace: true, max: MAX_NAME_LEN, message: `1–${MAX_NAME_LEN} 个字符` }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="address" label="地址" extra={SAME_MACHINE_ADDRESS_HINT} rules={[{ validator: validateHostAddressField }]}>
          <Input autoComplete="off" maxLength={MAX_HOST_ADDRESS_LEN} placeholder="IP 或域名" />
        </Form.Item>
        <Form.Item name="sshPort" label="SSH 端口" rules={[{ required: true, message: '请输入 SSH 端口' }]}>
          <InputNumber min={MIN_PORT} max={MAX_PORT} precision={0} className="full-width" />
        </Form.Item>
        <Form.Item name="credentialId" label="SSH 凭据" rules={[{ required: true, message: '请选择 SSH 凭据' }]}>
          <SshCredentialSelect onAuthTypeChange={secret.onAuthTypeChange} />
        </Form.Item>
        {secret.isNeeded && <CredentialSecretField extra="所选凭据是密码登录，改了凭据、地址或端口时须再输入一次密码，与已保存的一致才继续" />}
        <Form.Item name="region" label="位置">
          <Select
            showSearch={{ optionFilterProp: 'label' }}
            allowClear
            placeholder="选择国家或地区"
            options={COUNTRY_OPTIONS as { value: string; label: string }[]}
          />
        </Form.Item>
        <Form.Item name="panelPort" label="指定面板端口" extra="安装面板时用的端口；留空则随机">
          <InputNumber min={MIN_PORT} max={MAX_PORT} precision={0} className="full-width" placeholder="留空 = 随机" />
        </Form.Item>
        <Form.Item name="dnsAccountId" label="DNS 账号" extra="选了就由平台给面板申请公开可信的证书并自动续期；不选则面板用自签证书">
          <DnsAccountSelect />
        </Form.Item>
        {hasDnsAccount && (
          <Form.Item
            name="domain"
            label="域名"
            extra="须是所选 DNS 账号的根域名或它的子域名；留空则自动生成（地址是根域名下的域名就用地址，否则按名称）。改了域名或 DNS 账号后，要在 SSH 运维里重做一次“申请证书”才生效"
          >
            <Input autoComplete="off" maxLength={MAX_DOMAIN_LEN} placeholder="如 hk-01.example.com" />
          </Form.Item>
        )}
        <Button type="primary" htmlType="submit" loading={submit.isBusy}>
          保存
        </Button>
      </Form>
    </>
  )
}
