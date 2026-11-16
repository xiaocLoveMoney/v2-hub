import { Alert, Button, Form, Input, InputNumber, Select } from 'antd'
import { api, csrfHeader, type ServerHost } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { FormModal } from '../../components/FormModal'
import { useSubmit } from '../../hooks/use-submit'
import { MAX_NAME_LEN, MAX_PORT } from '../form-limits'
import { COUNTRY_OPTIONS } from './countries'
import { MAX_HOST_ADDRESS_LEN, MIN_PORT, validateHostAddressField } from './host-list'
import { SshCredentialSelect } from './SshCredentialSelect'

interface HostEditForm {
  name: string
  address: string
  sshPort: number
  credentialId: number
  region?: string
  panelPort?: number | null
}

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

  const handleFinish = (v: HostEditForm) =>
    submit.run(() =>
      api.PUT('/api/admin/server-hosts/{id}', {
        params: { ...csrfHeader(), path: { id: host.id } },
        body: {
          name: v.name.trim(),
          address: v.address.trim(),
          sshPort: v.sshPort,
          credentialId: v.credentialId,
          region: v.region ?? '',
          panelPort: v.panelPort ?? null,
        },
      }),
    )

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
        }}
      >
        <Form.Item name="name" label="名称" extra="接入时用作服务器名" rules={[{ required: true, whitespace: true, max: MAX_NAME_LEN, message: `1–${MAX_NAME_LEN} 个字符` }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="address" label="地址" rules={[{ validator: validateHostAddressField }]}>
          <Input autoComplete="off" maxLength={MAX_HOST_ADDRESS_LEN} placeholder="IP 或域名" />
        </Form.Item>
        <Form.Item name="sshPort" label="SSH 端口" rules={[{ required: true, message: '请输入 SSH 端口' }]}>
          <InputNumber min={MIN_PORT} max={MAX_PORT} precision={0} className="full-width" />
        </Form.Item>
        <Form.Item name="credentialId" label="SSH 凭据" rules={[{ required: true, message: '请选择 SSH 凭据' }]}>
          <SshCredentialSelect />
        </Form.Item>
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
        <Button type="primary" htmlType="submit" loading={submit.isBusy}>
          保存
        </Button>
      </Form>
    </>
  )
}
