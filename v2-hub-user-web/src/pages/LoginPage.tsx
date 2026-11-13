import { useState, type FormEvent } from 'react'
import { Link, Navigate, useLocation, useNavigate } from 'react-router'
import { CODE_TOTP_REQUIRED } from '../api/client'
import { useSession } from '../auth/session-context'
import { useI18n } from '../i18n/i18n-context'
import { Alert, Button, Field } from '../components/ui'
import { ROUTES } from '../menu'
import { useSite } from '../site/site-context'
import { AuthShell } from './auth/AuthShell'
import { MAX_CREDENTIAL_LEN, MAX_EMAIL_LEN, TOTP_LENGTH, digitsOnly } from './auth/fields'

const REGISTER_CLOSED = 'closed'

interface LocationState {
  from?: string
}

/**
 * 分屏登录页（docs/06 §5）：左侧表单，右侧全高背景与标语。
 * 口令通过但账号开启了两步验证时，在同一表单内展开验证码框，不跳页（docs/03 §5.3）。
 */
export function LoginPage() {
  const { status, login } = useSession()
  const site = useSite()
  const { t } = useI18n()
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
    <AuthShell lead={t('auth.loginLead')} subtitle={t('auth.loginSubtitle')}>
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <form onSubmit={(e) => void handleSubmit(e)} noValidate>
        {!needsTotp && (
          <>
            <Field
              label={t('auth.username')}
              name="username"
              autoComplete="username"
              required
              maxLength={MAX_EMAIL_LEN}
              hint={t('auth.usernameLoginHint')}
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
            <Field
              label={t('auth.password')}
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
            label={t('auth.totp')}
            name="totp"
            inputMode="numeric"
            autoComplete="one-time-code"
            autoFocus
            maxLength={TOTP_LENGTH}
            hint={t('auth.totpHint')}
            value={totpCode}
            onChange={(e) => setTotpCode(digitsOnly(e.target.value))}
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
          {t(needsTotp ? 'auth.verifyAndLogin' : 'auth.login')}
        </Button>
      </form>
      {!needsTotp && (
        <p className="hub-login-links">
          {site.registerMode !== REGISTER_CLOSED && <Link to={ROUTES.REGISTER}>{t('auth.registerAccount')}</Link>}
          {site.mailEnabled && <Link to={ROUTES.RESET}>{t('auth.forgot')}</Link>}
        </p>
      )}
    </AuthShell>
  )
}
