import { Alert, App, Button, Card, Form, Input, QRCode, Space, Tag, Typography } from 'antd'
import { useState } from 'react'
import { api, csrfHeader, toApiError, type ApiError } from '../api/client'
import { useSession } from '../auth/session-context'
import { mutate } from '../hooks/use-remote'

const TOTP_PATTERN = /^\d{6}$/
const QR_SIZE = 180

interface CodeForm {
  code: string
}

interface PendingSetup {
  secret: string
  otpauthUri: string
}

const codeRules = [{ required: true, pattern: TOTP_PATTERN, message: '请输入 6 位数字验证码' }]

/** 账号安全：开启 / 关闭本人的两步验证（docs/03 §4.3）。 */
export function SecurityPage() {
  const { session, refresh } = useSession()
  const { message } = App.useApp()
  const [pending, setPending] = useState<PendingSetup | null>(null)
  const [error, setError] = useState<ApiError | null>(null)
  const [isBusy, setIsBusy] = useState(false)
  const isEnabled = session?.totpEnabled ?? false

  const handleSetup = async () => {
    setIsBusy(true)
    setError(null)
    try {
      const { data, error: apiError } = await api.POST('/api/admin/auth/totp/setup', { params: csrfHeader() })
      if (apiError !== undefined || data?.data === undefined) {
        setError(toApiError(apiError))
        return
      }
      setPending(data.data)
    } catch (err) {
      reportError(err)
      setError(toApiError(err))
    } finally {
      setIsBusy(false)
    }
  }

  const submitCode = async (path: '/api/admin/auth/totp/enable' | '/api/admin/auth/totp/disable', values: CodeForm, okText: string) => {
    setIsBusy(true)
    setError(null)
    const err = await mutate(() => api.POST(path, { params: csrfHeader(), body: { code: values.code } }))
    setIsBusy(false)
    if (err !== null) {
      setError(err)
      return
    }
    setPending(null)
    void message.success(okText)
    await refresh()
  }

  return (
    <>
      <Typography.Title level={3}>账号安全</Typography.Title>
      <Card title={<Space>两步验证{isEnabled ? <Tag color="green">已开启</Tag> : <Tag>未开启</Tag>}</Space>} className="security-card">
        {error !== null && <Alert type="error" title={error.message} showIcon className="page-alert" />}

        {isEnabled && (
          <>
            <Typography.Paragraph>登录时需要输入验证器 App 中的 6 位验证码。关闭前请先输入当前验证码确认。</Typography.Paragraph>
            <Form<CodeForm> layout="inline" onFinish={(v) => submitCode('/api/admin/auth/totp/disable', v, '已关闭两步验证')}>
              <Form.Item name="code" rules={codeRules}>
                <Input placeholder="6 位验证码" inputMode="numeric" maxLength={6} autoComplete="one-time-code" />
              </Form.Item>
              <Button danger htmlType="submit" loading={isBusy}>
                关闭两步验证
              </Button>
            </Form>
          </>
        )}

        {!isEnabled && pending === null && (
          <>
            <Typography.Paragraph>开启后，登录除了密码还需要验证器 App（如 Google Authenticator、1Password）生成的验证码。</Typography.Paragraph>
            <Button type="primary" onClick={() => void handleSetup()} loading={isBusy}>
              开启两步验证
            </Button>
          </>
        )}

        {!isEnabled && pending !== null && (
          <Space orientation="vertical" size="middle">
            <Typography.Text>1. 用验证器 App 扫描二维码；无法扫码时手动输入密钥：</Typography.Text>
            <QRCode value={pending.otpauthUri} size={QR_SIZE} />
            <Typography.Text code copyable>
              {pending.secret}
            </Typography.Text>
            <Typography.Text>2. 输入 App 显示的验证码完成开启（密钥只显示这一次）：</Typography.Text>
            <Form<CodeForm> layout="inline" onFinish={(v) => submitCode('/api/admin/auth/totp/enable', v, '已开启两步验证')}>
              <Form.Item name="code" rules={codeRules}>
                <Input placeholder="6 位验证码" inputMode="numeric" maxLength={6} autoComplete="one-time-code" />
              </Form.Item>
              <Button type="primary" htmlType="submit" loading={isBusy}>
                确认开启
              </Button>
            </Form>
          </Space>
        )}
      </Card>
    </>
  )
}
