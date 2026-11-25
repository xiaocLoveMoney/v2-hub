import { Alert, Button, Form } from 'antd'
import { api, csrfHeader, type ServerHost } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { FormModal } from '../../components/FormModal'
import { useSubmit } from '../../hooks/use-submit'
import { CredentialSecretField } from './CredentialSecretField'
import { CREDENTIAL_SECRET_FIELD, credentialSecretPart } from './credential-secret'

interface HostResetKeyForm {
  credentialSecret?: string
}

interface HostResetKeyModalProps {
  /** 要重置主机密钥的主机（所用凭据是密码类型）；null 时弹窗关闭。 */
  host: ServerHost | null
  onClose: () => void
  /** 重置成功后调用（关闭弹窗、刷新列表）。 */
  onDone: () => void
}

/**
 * 重置主机密钥的确认弹窗，只用于密码凭据的主机（host:write）：重置后下一次连接重新首次信任，
 * 所以要再输入一次该凭据的密码（Ruling-97）。私钥凭据的主机仍用行内的二次确认。
 */
export function HostResetKeyModal({ host, onClose, onDone }: HostResetKeyModalProps) {
  return (
    <FormModal title={host === null ? '' : `重置 ${host.name} 的主机密钥`} isOpen={host !== null} onClose={onClose}>
      {(generation) => host !== null && <HostResetKeyBody key={generation} host={host} onDone={onDone} />}
    </FormModal>
  )
}

function HostResetKeyBody({ host, onDone }: { host: ServerHost; onDone: () => void }) {
  const [form] = Form.useForm<HostResetKeyForm>()
  const submit = useSubmit('已重置主机密钥', onDone)

  const handleFinish = async (v: HostResetKeyForm) => {
    form.setFieldValue(CREDENTIAL_SECRET_FIELD, undefined)
    await submit.run(() =>
      api.POST('/api/admin/server-hosts/{id}/reset-host-key', {
        params: { ...csrfHeader(), path: { id: host.id } },
        body: credentialSecretPart(true, v.credentialSecret),
      }),
    )
  }

  return (
    <>
      <ErrorAlert error={submit.error} />
      <Alert type="warning" showIcon className="page-alert" title="清空已固定的主机密钥，下次连接时重新记录。只在确认机器确实重装过之后做" />
      <Form<HostResetKeyForm> form={form} layout="vertical" onFinish={handleFinish}>
        <CredentialSecretField extra="这台主机的凭据是密码登录，重置后下一次连接会接受任何主机密钥并把密码发过去，所以须再输入一次密码" />
        <Button type="primary" danger htmlType="submit" loading={submit.isBusy}>
          重置主机密钥
        </Button>
      </Form>
    </>
  )
}
