import { CheckOutlined, LockOutlined, SafetyOutlined, UserOutlined } from '@ant-design/icons'
import { Alert, Button, Card, Form, Input, Typography } from 'antd'
import { useState } from 'react'
import { Navigate, useLocation } from 'react-router'
import { landingPath } from '../agent-menu'
import { CODE_TOTP_REQUIRED } from '../api/client'
import authBackground from '../assets/auth-bg.webp'
import { useSession } from '../auth/session-context'
import { revealChildren } from '../components/motion'
import { BrandMark } from '../components/BrandMark'

interface LoginForm {
  username: string
  credential: string
  totpCode?: string
}

interface LocationState {
  from?: string
}

/** 顶栏品牌位的图标尺寸（像素）。 */
const LOGIN_MARK_SIZE = 28
/** 版权年份：页面加载时取一次即可。 */
const COPYRIGHT_YEAR = new Date(Date.now()).getFullYear()
/** 右侧的三条功能亮点（docs/05 登录页）。 */
const FEATURES = ['服务器、入站与节点集中管理', '用户、套餐与订单统一运营', '链式规则下发与对账一目了然'] as const
const TOTP_PATTERN = /^\d{6}$/

/** 管理员登录页（管理门户入口）。账号开启两步验证时，密码通过后再显示验证码输入框。 */
export function LoginPage() {
  const { status, session, login } = useSession()
  const location = useLocation()
  const [errorMessage, setErrorMessage] = useState<string | null>(null)
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [needsTotp, setNeedsTotp] = useState(false)
  const from = (location.state as LocationState | null)?.from

  // 登录成功后会话一更新就在这里跳走；落地页按账号类型区分（Ruling-101）
  if (status === 'authenticated' && session !== null) {
    return <Navigate to={landingPath(session.kind, from)} replace />
  }

  const handleFinish = async (values: LoginForm) => {
    setIsSubmitting(true)
    setErrorMessage(null)
    const err = await login(values.username, values.credential, needsTotp ? values.totpCode : undefined)
    setIsSubmitting(false)
    if (err === null) {
      return
    }
    if (err.code === CODE_TOTP_REQUIRED) {
      setNeedsTotp(true)
      return
    }
    setErrorMessage(err.message)
  }

  return (
    <div className="login-page" style={{ backgroundImage: `url(${authBackground})` }}>
      <header className="login-top">
        <span className="login-top-brand">
          <BrandMark size={LOGIN_MARK_SIZE} motion="intro" hasHoverTurn />
          <span>v2-hub</span>
        </span>
        <span className="login-top-divider" aria-hidden="true" />
        <span className="login-top-label">管理门户</span>
      </header>
      <main className="login-main">
        <Card className="login-card" ref={revealChildren}>
          <Typography.Title level={3}>v2-hub 管理端</Typography.Title>
          <Typography.Paragraph type="secondary" className="login-subtitle">
            请使用管理员账号登录
          </Typography.Paragraph>
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
        <section className="login-pitch" aria-label="平台介绍">
          <h2>v2-hub</h2>
          <p className="login-slogan">多台 3x-ui 面板，一处管好</p>
          <ul className="login-features">
            {FEATURES.map((text) => (
              <li key={text}>
                <CheckOutlined aria-hidden="true" />
                <span>{text}</span>
              </li>
            ))}
          </ul>
        </section>
      </main>
      <footer className="login-footer">© {COPYRIGHT_YEAR} v2-hub</footer>
    </div>
  )
}
