import { Button, Form, Input, InputNumber, Modal, Select } from 'antd'
import { api, csrfHeader, type Inbound } from '../../api/client'
import { useSubmit } from '../../hooks/use-submit'
import { ErrorAlert } from '../../components/ErrorAlert'
import { MAX_PORT } from '../form-limits'

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
  const { error, isBusy, run } = useSubmit('已添加对外地址', onSaved)

  const handleFinish = (v: EndpointForm) =>
    run(() =>
      api.POST('/api/admin/servers/{id}/endpoints', {
        params: { ...csrfHeader(), path: { id: serverId } },
        body: { inboundIds: v.inboundIds, addresses: v.addresses, port: v.port ?? 0, remark: v.remark ?? '' },
      }),
    )

  return (
    <Modal title="添加对外地址（Host）" open={isOpen} onCancel={onClose} footer={null} destroyOnHidden>
      <ErrorAlert error={error} />
      <Form<EndpointForm> layout="vertical" onFinish={handleFinish}>
        <Form.Item name="inboundIds" label="入站" rules={[{ required: true, message: '至少选一个入站' }]}>
          <Select
            mode="multiple"
            optionFilterProp="label"
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
