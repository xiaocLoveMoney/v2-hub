import { Button, Form, Input, Radio } from 'antd'
import { api, csrfHeader, type SshAuthType, type SshCredential, type SshCredentialInput } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useSubmit } from '../../hooks/use-submit'
import { MAX_NAME_LEN, MAX_REMARK_LEN } from '../form-limits'
import { SSH_AUTH_TYPE_LABELS } from './host-labels'

const AUTH_KEY: SshAuthType = 'key'
const AUTH_OPTIONS = (Object.keys(SSH_AUTH_TYPE_LABELS) as SshAuthType[]).map((value) => ({ value, label: SSH_AUTH_TYPE_LABELS[value] }))
/** 登录用户名的写法（hub-api.yaml SshCredentialInput.username）。 */
const USERNAME_PATTERN = /^[a-zA-Z_][a-zA-Z0-9_.-]*$/
const DEFAULT_USERNAME = 'root'
/** 私钥的最大长度：16 KB（docs/03 §5.22 SSH 凭据）。 */
const MAX_SECRET_LEN = 16384
const MAX_PASSPHRASE_LEN = 255
const PRIVATE_KEY_ROWS = 6
const KEEP_PLACEHOLDER = '留空 = 沿用原值；已保存的内容不会显示'
const KEY_PLACEHOLDER = '粘贴私钥文件的全部内容'

interface CredentialForm {
  name: string
  username: string
  authType: SshAuthType
  secret?: string
  passphrase?: string
  remark?: string
}

/**
 * 表单值转请求体。私钥 / 密码与口令只写：留空就不带这个字段，表示沿用原值（hub-api.yaml SshCredentialInput）；
 * 密码认证没有口令，不带 passphrase。
 */
function toInput(v: CredentialForm): SshCredentialInput {
  const { secret = '', passphrase = '' } = v
  const input: SshCredentialInput = { name: v.name.trim(), username: v.username.trim(), authType: v.authType, remark: v.remark ?? '' }
  if (secret !== '') {
    input.secret = secret
  }
  if (v.authType === AUTH_KEY && passphrase !== '') {
    input.passphrase = passphrase
  }
  return input
}

interface SshCredentialFormProps {
  /** null 表示新建。 */
  credential: SshCredential | null
  /** 保存成功后调用（关闭表单、刷新列表）。 */
  onSaved: () => void
}

/**
 * 新建 / 编辑 SSH 凭据的表单（host:write）。私钥、密码、口令只写不读：接口不返回，表单也不回显，
 * 编辑时留空表示沿用原值；改了认证方式则必须重新填（docs/03 §5.22 SSH 凭据）。
 */
export function SshCredentialForm({ credential, onSaved }: SshCredentialFormProps) {
  const [form] = Form.useForm<CredentialForm>()
  const isEdit = credential !== null
  const submit = useSubmit(isEdit ? '已保存' : '已新建', onSaved)
  const authType = Form.useWatch('authType', form) ?? credential?.authType ?? AUTH_KEY
  const isKey = authType === AUTH_KEY
  // 新建，或编辑时换了认证方式：必须填新的私钥 / 密码
  const isSecretRequired = !isEdit || authType !== credential.authType
  const secretLabel = isKey ? '私钥' : '密码'

  /** 换认证方式时清掉已填的私钥 / 密码，免得把私钥当密码提交。 */
  const handleAuthTypeChange = () => form.setFieldsValue({ secret: '', passphrase: '' })

  const handleFinish = (v: CredentialForm) => {
    const body = toInput(v)
    return submit.run(() =>
      isEdit
        ? api.PUT('/api/admin/ssh-credentials/{id}', { params: { ...csrfHeader(), path: { id: credential.id } }, body })
        : api.POST('/api/admin/ssh-credentials', { params: csrfHeader(), body }),
    )
  }

  return (
    <>
      <ErrorAlert error={submit.error} />
      <Form<CredentialForm>
        form={form}
        layout="vertical"
        onFinish={handleFinish}
        initialValues={{
          name: credential?.name ?? '',
          username: credential?.username ?? DEFAULT_USERNAME,
          authType: credential?.authType ?? AUTH_KEY,
          remark: credential?.remark ?? '',
        }}
      >
        <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true, max: MAX_NAME_LEN, message: `1–${MAX_NAME_LEN} 个字符` }]}>
          <Input autoComplete="off" placeholder="如 root-key-2026" />
        </Form.Item>
        <Form.Item
          name="username"
          label="用户名"
          extra="须是 root 或能免密 sudo 的用户"
          rules={[{ required: true, max: MAX_NAME_LEN, pattern: USERNAME_PATTERN, message: '字母或下划线开头，只含字母、数字、“_”“.”“-”，最长 64' }]}
        >
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="authType" label="认证方式">
          <Radio.Group options={AUTH_OPTIONS} onChange={handleAuthTypeChange} />
        </Form.Item>
        <Form.Item
          name="secret"
          label={secretLabel}
          extra={isKey ? 'OpenSSH 或 PEM 格式，不超过 16 KB；保存后加密存储，不再显示' : '保存后加密存储，不再显示'}
          rules={[{ required: isSecretRequired, message: `请输入${secretLabel}` }]}
        >
          {isKey ? (
            <Input.TextArea
              rows={PRIVATE_KEY_ROWS}
              className="mono"
              autoComplete="off"
              spellCheck={false}
              maxLength={MAX_SECRET_LEN}
              placeholder={isSecretRequired ? KEY_PLACEHOLDER : KEEP_PLACEHOLDER}
            />
          ) : (
            <Input.Password autoComplete="new-password" maxLength={MAX_SECRET_LEN} placeholder={isSecretRequired ? '' : KEEP_PLACEHOLDER} />
          )}
        </Form.Item>
        {isKey && (
          <Form.Item name="passphrase" label="私钥口令" extra="私钥没有加口令就留空">
            <Input.Password autoComplete="new-password" maxLength={MAX_PASSPHRASE_LEN} placeholder={isSecretRequired ? '' : KEEP_PLACEHOLDER} />
          </Form.Item>
        )}
        <Form.Item name="remark" label="备注" rules={[{ max: MAX_REMARK_LEN, message: `最长 ${MAX_REMARK_LEN} 个字符` }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Button type="primary" htmlType="submit" loading={submit.isBusy}>
          {isEdit ? '保存' : '新建'}
        </Button>
      </Form>
    </>
  )
}
