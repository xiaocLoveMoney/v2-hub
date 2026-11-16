import { App, Button, Form, Input, InputNumber, Typography } from 'antd'
import { useState } from 'react'
import { api, csrfHeader, toApiError, type ApiError, type Server, type ServerHost } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { FormModal } from '../../components/FormModal'
import { MAX_PORT, MIN_PORT } from '../form-limits'
import { DEFAULT_SSH_PORT, MAX_HOST_ADDRESS_LEN, hostnameOfUrl, validateHostAddressField } from './host-list'
import { SshCredentialSelect } from './SshCredentialSelect'

interface HostBindForm {
  address: string
  sshPort: number
  credentialId: number
}

type BoundServer = Pick<Server, 'id' | 'name' | 'baseUrl'>

interface HostBindModalProps {
  server: BoundServer
  isOpen: boolean
  onClose: () => void
  /** 绑定成功后调用，参数是新建的主机（已有一条排队中的校验任务）。 */
  onBound: (host: ServerHost) => void
}

/**
 * 给已接入的服务器绑定 SSH 的弹窗（host:write）：地址默认取面板地址里的主机名，保存后排一条校验连接的任务
 * （docs/03 §5.22 给已接入的服务器绑定 SSH）。
 */
export function HostBindModal({ server, isOpen, onClose, onBound }: HostBindModalProps) {
  return (
    <FormModal title={`绑定 SSH：${server.name}`} isOpen={isOpen} onClose={onClose}>
      {(generation) => <HostBindBody key={generation} server={server} onBound={onBound} />}
    </FormModal>
  )
}

function HostBindBody({ server, onBound }: Pick<HostBindModalProps, 'server' | 'onBound'>) {
  const [form] = Form.useForm<HostBindForm>()
  const { message } = App.useApp()
  const [error, setError] = useState<ApiError | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const handleFinish = async (v: HostBindForm) => {
    setIsBusy(true)
    setError(null)
    try {
      const { data, error: apiError } = await api.PUT('/api/admin/servers/{id}/host', {
        params: { ...csrfHeader(), path: { id: server.id } },
        body: { address: v.address.trim(), sshPort: v.sshPort, credentialId: v.credentialId },
      })
      if (apiError !== undefined || data?.data === undefined) {
        setError(toApiError(apiError))
        return
      }
      void message.success('已绑定，正在校验连接')
      onBound(data.data)
    } catch (err) {
      reportError(err)
      setError(toApiError(err))
    } finally {
      setIsBusy(false)
    }
  }

  return (
    <>
      <ErrorAlert error={error} />
      <Form<HostBindForm>
        form={form}
        layout="vertical"
        onFinish={handleFinish}
        initialValues={{ address: hostnameOfUrl(server.baseUrl), sshPort: DEFAULT_SSH_PORT }}
      >
        <Form.Item name="address" label="地址" extra="默认取面板地址里的主机名；面板走 CDN 或反代时请改成机器的真实地址" rules={[{ validator: validateHostAddressField }]}>
          <Input autoComplete="off" maxLength={MAX_HOST_ADDRESS_LEN} placeholder="IP 或域名" />
        </Form.Item>
        <Form.Item name="sshPort" label="SSH 端口" rules={[{ required: true, message: '请输入 SSH 端口' }]}>
          <InputNumber min={MIN_PORT} max={MAX_PORT} precision={0} className="full-width" />
        </Form.Item>
        <Form.Item name="credentialId" label="SSH 凭据" extra="登录用户须是 root 或能免密 sudo" rules={[{ required: true, message: '请选择 SSH 凭据' }]}>
          <SshCredentialSelect />
        </Form.Item>
        <Button type="primary" htmlType="submit" loading={isBusy}>
          绑定并校验
        </Button>
        <Typography.Paragraph type="secondary" className="form-hint">
          保存后平台用这份凭据连一次：能连上并检测到标准安装的面板即成功。主机名取服务器名。
        </Typography.Paragraph>
      </Form>
    </>
  )
}
