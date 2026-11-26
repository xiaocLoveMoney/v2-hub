import { Form, Input } from 'antd'
import { CREDENTIAL_SECRET_FIELD } from './credential-secret'

/** 再次输入的密码的最大长度（hub-api.yaml credentialSecret）。 */
const MAX_CREDENTIAL_SECRET_LEN = 255

interface CredentialSecretFieldProps {
  /** 输入框下的说明。 */
  extra?: string
}

/**
 * “再次输入该凭据的密码”（Ruling-97，docs/03 §5.22 密码凭据的确认）：放在 Form 里用，必填、密码框、不回显、不让浏览器记住。
 * 提交后由调用方清空。
 */
export function CredentialSecretField({ extra }: CredentialSecretFieldProps) {
  return (
    <Form.Item
      name={CREDENTIAL_SECRET_FIELD}
      label="再次输入该凭据的密码"
      extra={extra ?? '所选凭据是密码登录，用到还没用过它的地址时须再输入一次密码，与已保存的一致才继续'}
      rules={[{ required: true, message: '请输入该凭据的密码' }]}
    >
      <Input.Password autoComplete="off" visibilityToggle={false} maxLength={MAX_CREDENTIAL_SECRET_LEN} />
    </Form.Item>
  )
}
