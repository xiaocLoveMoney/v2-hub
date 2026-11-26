import { App, Button, Card, Form, Input, Typography } from 'antd'
import { useState } from 'react'
import { api, csrfHeader, type ApiError } from '../api/client'
import { ErrorAlert } from '../components/ErrorAlert'
import { mutate } from '../hooks/use-remote'
import { MAX_ADMIN_PASSWORD_LEN } from './form-limits'
import { ADMIN_CREDENTIAL_LENGTH_RULE } from './system/admin-form-rules'

interface ChangePasswordForm {
  current: string
  next: string
  confirm: string
}

const nextRules = [{ required: true, message: '请输入新密码' }, ADMIN_CREDENTIAL_LENGTH_RULE]

/** 账号安全页的“修改密码”卡片（R9）：须填旧密码；成功后当前会话保留、其它设备下线（R7）。旧密码错误就地提示。 */
export function ChangePasswordCard() {
  const [form] = Form.useForm<ChangePasswordForm>()
  const { message } = App.useApp()
  const [error, setError] = useState<ApiError | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const handleFinish = async (values: ChangePasswordForm) => {
    setIsBusy(true)
    setError(null)
    const prev = values.current
    const next = values.next
    const err = await mutate(() => api.PUT('/api/admin/auth/password', { params: csrfHeader(), body: { oldPassword: prev, newPassword: next } }))
    setIsBusy(false)
    // 无论成败都清空，密码不留在表单里
    form.resetFields()
    if (err !== null) {
      setError(err)
      return
    }
    void message.success('密码已修改，其它设备上的登录已下线')
  }

  return (
    <Card title="修改密码" className="security-card">
      <ErrorAlert error={error} />
      <Typography.Paragraph type="secondary">修改后当前登录保留，其它设备上的登录会全部下线。</Typography.Paragraph>
      <Form<ChangePasswordForm> form={form} layout="vertical" onFinish={handleFinish}>
        <Form.Item name="current" label="旧密码" rules={[{ required: true, message: '请输入旧密码' }]}>
          <Input.Password maxLength={MAX_ADMIN_PASSWORD_LEN} autoComplete="current-password" />
        </Form.Item>
        <Form.Item name="next" label="新密码" rules={nextRules}>
          <Input.Password autoComplete="new-password" />
        </Form.Item>
        <Form.Item
          name="confirm"
          label="确认新密码"
          dependencies={['next']}
          rules={[
            { required: true, message: '请再输入一次新密码' },
            ({ getFieldValue }) => ({
              validator: async (_: unknown, value: string | undefined) => {
                if (value !== undefined && value !== '' && value !== getFieldValue('next')) {
                  throw new Error('两次输入的新密码不一致')
                }
              },
            }),
          ]}
        >
          <Input.Password autoComplete="new-password" />
        </Form.Item>
        <Button type="primary" htmlType="submit" loading={isBusy}>
          修改密码
        </Button>
      </Form>
    </Card>
  )
}
