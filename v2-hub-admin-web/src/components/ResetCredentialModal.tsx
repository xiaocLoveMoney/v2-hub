import { Button, Form, Input, type FormItemProps } from 'antd'
import { useSubmit } from '../hooks/use-submit'
import { ErrorAlert } from './ErrorAlert'
import { FormModal } from './FormModal'

/** 被重置密码的账号：至少有 id 与用户名（终端用户、代理名下用户、代理账号都满足）。 */
interface CredentialTarget {
  id: number
  username: string
}

/**
 * 重置密码的写操作：各处只差接口（员工改终端用户、代理改名下用户、超管改代理账号）。
 * @param target 被重置的账号
 * @param credential 新密码
 */
export type ResetCredentialSave<T extends CredentialTarget> = (target: T, credential: string) => Promise<{ error?: unknown }>

interface ResetCredentialModalProps<T extends CredentialTarget> {
  /** 被重置的账号；null 时弹窗关闭。 */
  target: T | null
  /** 新密码输入框的校验（终端用户 8 位起、管理端账号 12 位起）。 */
  rules: FormItemProps['rules']
  /** 成功提示。 */
  okText: string
  save: ResetCredentialSave<T>
  onClose: () => void
  onSaved: () => void
}

/** 重置登录密码的弹窗：一个新密码输入框 + 重置按钮；失败留在弹窗里提示。高危接口的二次验证由 api 客户端统一弹框。 */
export function ResetCredentialModal<T extends CredentialTarget>({ target, rules, okText, save, onClose, onSaved }: ResetCredentialModalProps<T>) {
  return (
    <FormModal title={`重置密码：${target?.username ?? ''}`} isOpen={target !== null} onClose={onClose}>
      {(generation) => target !== null && <ResetCredentialForm key={generation} target={target} rules={rules} okText={okText} save={save} onSaved={onSaved} />}
    </FormModal>
  )
}

function ResetCredentialForm<T extends CredentialTarget>({ target, rules, okText, save, onSaved }: Omit<ResetCredentialModalProps<T>, 'target' | 'onClose'> & { target: T }) {
  const { error, isBusy, run } = useSubmit(okText, onSaved)
  const submit = (v: { credential: string }) => run(() => save(target, v.credential))
  return (
    <>
      <ErrorAlert error={error} />
      <Form layout="vertical" onFinish={(v) => void submit(v)}>
        <Form.Item name="credential" label="新密码" rules={rules}>
          <Input.Password autoComplete="new-password" />
        </Form.Item>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          重置
        </Button>
      </Form>
    </>
  )
}
