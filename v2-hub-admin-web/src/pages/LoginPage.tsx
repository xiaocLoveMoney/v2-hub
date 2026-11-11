import { LockOutlined, UserOutlined } from '@ant-design/icons'
import { Alert, Button, Card, Form, Input, Typography } from 'antd'
import { useState } from 'react'
import { Navigate, useLocation, useNavigate } from 'react-router'
import { useSession } from '../auth/session-context'
import { ROUTES } from '../menu'

interface LoginForm {
  username: string
  credential: string
}

interface LocationState {
  from?: string
}

/** 管理员登录页（管理门户入口）。 */
export function LoginPage() {
  const { status, login } = useSession()
  const navigate = useNavigate()
  const location = useLocation()
  const [errorMessage, setErrorMessage] = useState<string | null>(null)
  const [isSubmitting, setIsSubmitting] = useState(false)
  const target = (location.state as LocationState | null)?.from ?? ROUTES.DASHBOARD

  if (status === 'authenticated') {
    return <Navigate to={target} replace />
  }

  const handleFinish = async (values: LoginForm) => {
    setIsSubmitting(true)
    setErrorMessage(null)
    const err = await login(values.username, values.credential)
    setIsSubmitting(false)
    if (err !== null) {
      setErrorMessage(err.message)
      return
    }
    void navigate(target, { replace: true })
  }

  return (
    <main className="login-page">
      <Card className="login-card">
        <Typography.Title level={3}>v2-hub 管理端</Typography.Title>
        {errorMessage !== null && <Alert type="error" title={errorMessage} showIcon className="login-alert" />}
        <Form<LoginForm> layout="vertical" onFinish={handleFinish} requiredMark={false}>
          <Form.Item name="username" label="用户名" rules={[{ required: true, message: '请输入用户名' }]}>
            <Input prefix={<UserOutlined />} autoComplete="username" />
          </Form.Item>
          <Form.Item name="credential" label="密码" rules={[{ required: true, message: '请输入密码' }]}>
            <Input.Password prefix={<LockOutlined />} autoComplete="current-password" />
          </Form.Item>
          <Button type="primary" htmlType="submit" block loading={isSubmitting}>
            登录
          </Button>
        </Form>
      </Card>
    </main>
  )
}
