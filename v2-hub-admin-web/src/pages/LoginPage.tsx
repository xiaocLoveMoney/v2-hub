import { LockOutlined, SafetyOutlined, UserOutlined } from '@ant-design/icons'
import { Alert, Button, Card, Form, Input, Typography } from 'antd'
import { useState } from 'react'
import { Navigate, useLocation, useNavigate } from 'react-router'
import { CODE_TOTP_REQUIRED } from '../api/client'
import { useSession } from '../auth/session-context'
import { revealChildren } from '../components/motion'
import { ROUTES } from '../menu'
import { BrandMark } from '../components/BrandMark'

interface LoginForm {
  username: string
  credential: string
  totpCode?: string
}

interface LocationState {
  from?: string
}

/** 登录卡片标题前的图标尺寸（像素）。 */
const LOGIN_MARK_SIZE = 36
const TOTP_PATTERN = /^\d{6}$/

/** 管理员登录页（管理门户入口）。账号开启两步验证时，密码通过后再显示验证码输入框。 */
export function LoginPage() {
  const { status, login } = useSession()
  const navigate = useNavigate()
  const location = useLocation()
  const [errorMessage, setErrorMessage] = useState<string | null>(null)
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [needsTotp, setNeedsTotp] = useState(false)
  const target = (location.state as LocationState | null)?.from ?? ROUTES.DASHBOARD

  if (status === 'authenticated') {
    return <Navigate to={target} replace />
  }

  const handleFinish = async (values: LoginForm) => {
    setIsSubmitting(true)
    setErrorMessage(null)
    const err = await login(values.username, values.credential, needsTotp ? values.totpCode : undefined)
    setIsSubmitting(false)
    if (err === null) {
      void navigate(target, { replace: true })
      return
    }
    if (err.code === CODE_TOTP_REQUIRED) {
      setNeedsTotp(true)
      return
    }
    setErrorMessage(err.message)
  }

  return (
    <main className="login-page" ref={revealChildren}>
      <Card className="login-card">
        <div className="login-brand">
          <BrandMark size={LOGIN_MARK_SIZE} motion="intro" hasHoverTurn />
          <Typography.Title level={3}>v2-hub 管理端</Typography.Title>
        </div>
        {errorMessage !== null && <Alert type="error" title={errorMessage} showIcon className="login-alert" />}
        <Form<LoginForm> layout="vertical" onFinish={handleFinish} requiredMark={false}>
          <Form.Item name="username" label="用户名" rules={[{ required: true, message: '请输入用户名' }]}>
            <Input prefix={<UserOutlined />} autoComplete="username" disabled={needsTotp} />
          </Form.Item>
          <Form.Item name="credential" label="密码" rules={[{ required: true, message: '请输入密码' }]}>
            <Input.Password prefix={<LockOutlined />} autoComplete="current-password" disabled={needsTotp} />
          </Form.Item>
          {needsTotp && (
            <Form.Item
              name="totpCode"
              label="两步验证码"
              extra="打开验证器 App，输入当前显示的 6 位数字"
              rules={[{ required: true, pattern: TOTP_PATTERN, message: '请输入 6 位数字验证码' }]}
            >
              <Input prefix={<SafetyOutlined />} inputMode="numeric" autoComplete="one-time-code" maxLength={6} autoFocus />
            </Form.Item>
          )}
          <Button type="primary" htmlType="submit" block loading={isSubmitting}>
            {needsTotp ? '验证' : '登录'}
          </Button>
        </Form>
      </Card>
    </main>
  )
}
