import { useState, type FormEvent } from 'react'
import { Navigate, useLocation, useNavigate } from 'react-router'
import { CODE_TOTP_REQUIRED } from '../api/client'
import { BRAND, LOGIN_SLOGAN } from '../app-config'
import { useSession } from '../auth/session-context'
import { Alert, Button, Field } from '../components/ui'
import { ROUTES } from '../menu'

const TOTP_LENGTH = 6
const MAX_USERNAME_LEN = 64
const MAX_CREDENTIAL_LEN = 128

interface LocationState {
  from?: string
}

/**
 * 分屏登录页（docs/06 §5）：左侧表单，右侧全高背景与标语。
 * 口令通过但账号开启了两步验证时，在同一表单内展开验证码框，不跳页（docs/03 §5.3）。
 */
export function LoginPage() {
  const { status, login } = useSession()
  const navigate = useNavigate()
  const location = useLocation()
  const [username, setUsername] = useState('')
  const [credential, setCredential] = useState('')
  const [totpCode, setTotpCode] = useState('')
  const [needsTotp, setNeedsTotp] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  if (status === 'authenticated') {
    return <Navigate to={ROUTES.DASHBOARD} replace />
  }

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    setIsBusy(true)
    setError(null)
    const err = await login(username.trim(), credential, needsTotp ? totpCode : undefined)
    setIsBusy(false)
    if (err === null) {
      const from = (location.state as LocationState | null)?.from
      void navigate(from !== undefined && from.startsWith('/') ? from : ROUTES.DASHBOARD, { replace: true })
      return
    }
    if (err.code === CODE_TOTP_REQUIRED) {
      setNeedsTotp(true)
      return
    }
    setError(err.message)
  }

  return (
    <div className="hub-login">
      <section className="hub-login-form">
        <h1>
          欢迎使用 <strong>{BRAND}</strong>
        </h1>
        <p className="hub-muted" style={{ marginBottom: 28 }}>
          登录后查看订阅、节点与用量
        </p>
        {error !== null && <Alert tone="plain">{error}</Alert>}
        <form onSubmit={(e) => void handleSubmit(e)} noValidate>
          {!needsTotp && (
            <>
              <Field label="用户名" name="username" autoComplete="username" required maxLength={MAX_USERNAME_LEN} value={username} onChange={(e) => setUsername(e.target.value)} />
              <Field
                label="密码"
                name="password"
                type="password"
                autoComplete="current-password"
                required
                maxLength={MAX_CREDENTIAL_LEN}
                value={credential}
                onChange={(e) => setCredential(e.target.value)}
              />
            </>
          )}
          {needsTotp && (
            <Field
              label="两步验证码"
              name="totp"
              inputMode="numeric"
              autoComplete="one-time-code"
              autoFocus
              maxLength={TOTP_LENGTH}
              hint="打开验证器 App，输入 6 位数字"
              value={totpCode}
              onChange={(e) => setTotpCode(e.target.value.replace(/\D/g, ''))}
            />
          )}
          <Button
            type="submit"
            variant="primary"
            isLarge
            isBlock
            isBusy={isBusy}
            disabled={needsTotp ? totpCode.length !== TOTP_LENGTH : username.trim() === '' || credential === ''}
          >
            {needsTotp ? '验证并登录' : '登录'}
          </Button>
        </form>
      </section>
      <section className="hub-login-art" aria-hidden="true">
        <div>
          <h2>{BRAND}</h2>
          <p>{LOGIN_SLOGAN}</p>
        </div>
      </section>
    </div>
  )
}
