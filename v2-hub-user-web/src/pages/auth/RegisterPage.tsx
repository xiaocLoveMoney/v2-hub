import { useState, type FormEvent } from 'react'
import { Link, Navigate, useNavigate, useSearchParams } from 'react-router'
import { api, toApiError, type ApiError, type RegisterInput } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { Alert, Button, Field } from '../../components/ui'
import { mutate } from '../../hooks/use-remote'
import { useI18n } from '../../i18n/i18n-context'
import { ROUTES } from '../../menu'
import { useSite } from '../../site/site-context'
import { AuthShell } from './AuthShell'
import { EmailCodeField } from './EmailCodeField'
import {
  EMAIL_CODE_LENGTH, INVITE_QUERY_KEY, MAX_CREDENTIAL_LEN, MAX_EMAIL_LEN, MAX_INVITE_CODE_LEN, MAX_USERNAME_LEN, MIN_CREDENTIAL_LEN, USERNAME_PATTERN, isEmailLike,
} from './fields'

const MODE_CLOSED = 'closed'
const MODE_INVITE = 'invite'

const sendRegisterCode = (email: string): Promise<ApiError | null> =>
  mutate(() => api.POST('/api/user/auth/email-code', { body: { email, purpose: 'register' } }))

/**
 * 自助注册（docs/03 §5.8）：仅邀请时邀请码必填，开放注册时可选；要求验证邮箱时先发验证码。注册成功即登录。
 * 注册关闭时只显示说明。
 */
export function RegisterPage() {
  const site = useSite()
  const { t } = useI18n()
  const { status, adopt } = useSession()
  const navigate = useNavigate()
  const [query] = useSearchParams()
  const [username, setUsername] = useState('')
  const [email, setEmail] = useState('')
  const [credential, setCredential] = useState('')
  const [repeat, setRepeat] = useState('')
  const [inviteCode, setInviteCode] = useState(() => (query.get(INVITE_QUERY_KEY) ?? '').slice(0, MAX_INVITE_CODE_LEN))
  const [emailCode, setEmailCode] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  if (status === 'authenticated') {
    return <Navigate to={ROUTES.DASHBOARD} replace />
  }
  if (site.registerMode === MODE_CLOSED) {
    return (
      <AuthShell lead={t('auth.registerLead')} subtitle={t('auth.registerSubtitle')}>
        <Alert tone="info">{t('auth.registerClosed')}</Alert>
        <p className="hub-login-links">
          <Link to={ROUTES.LOGIN}>{t('common.backToLogin')}</Link>
        </p>
      </AuthShell>
    )
  }

  const isInviteRequired = site.registerMode === MODE_INVITE
  const isUsernameBad = username !== '' && !USERNAME_PATTERN.test(username)
  const isTooShort = credential !== '' && credential.length < MIN_CREDENTIAL_LEN
  const isMismatch = repeat !== '' && repeat !== credential
  const canSubmit =
    USERNAME_PATTERN.test(username) &&
    isEmailLike(email.trim()) &&
    credential.length >= MIN_CREDENTIAL_LEN &&
    repeat === credential &&
    (!isInviteRequired || inviteCode.trim() !== '') &&
    (!site.emailVerifyRequired || emailCode.length === EMAIL_CODE_LENGTH)

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    setIsBusy(true)
    setError(null)
    const pw = credential
    const body: RegisterInput = { username, email: email.trim(), password: pw }
    if (inviteCode.trim() !== '') {
      body.inviteCode = inviteCode.trim()
    }
    if (site.emailVerifyRequired) {
      body.emailCode = emailCode
    }
    try {
      const res = await api.POST('/api/user/auth/register', { body })
      if (res.error !== undefined || res.data?.data === undefined) {
        setError(toApiError(res.error).message)
        return
      }
      adopt(res.data.data)
      void navigate(ROUTES.DASHBOARD, { replace: true })
    } catch (err) {
      reportError(err)
      setError(toApiError(err).message)
    } finally {
      setIsBusy(false)
    }
  }

  return (
    <AuthShell lead={t('auth.registerLead')} subtitle={t('auth.registerSubtitle')}>
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <form onSubmit={(e) => void handleSubmit(e)} noValidate>
        <Field
          label={t('auth.username')}
          name="username"
          autoComplete="username"
          maxLength={MAX_USERNAME_LEN}
          hint={t('auth.usernameHint')}
          error={isUsernameBad ? t('auth.usernameInvalid') : null}
          value={username}
          onChange={(e) => setUsername(e.target.value.trim())}
        />
        <Field
          label={t('auth.email')}
          name="email"
          type="email"
          autoComplete="email"
          maxLength={MAX_EMAIL_LEN}
          hint={t('auth.emailRegisterHint')}
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />
        {site.emailVerifyRequired && <EmailCodeField email={email} value={emailCode} onChange={setEmailCode} send={sendRegisterCode} />}
        <Field
          label={t('auth.password')}
          name="password"
          type="password"
          autoComplete="new-password"
          maxLength={MAX_CREDENTIAL_LEN}
          hint={t('auth.passwordHint', { min: MIN_CREDENTIAL_LEN, max: MAX_CREDENTIAL_LEN })}
          error={isTooShort ? t('auth.passwordTooShort', { min: MIN_CREDENTIAL_LEN }) : null}
          value={credential}
          onChange={(e) => setCredential(e.target.value)}
        />
        <Field
          label={t('auth.passwordRepeat')}
          name="repeat"
          type="password"
          autoComplete="new-password"
          maxLength={MAX_CREDENTIAL_LEN}
          error={isMismatch ? t('auth.passwordMismatch') : null}
          value={repeat}
          onChange={(e) => setRepeat(e.target.value)}
        />
        <Field
          label={t(isInviteRequired ? 'auth.inviteCode' : 'auth.inviteCodeOptional')}
          name="inviteCode"
          autoComplete="off"
          maxLength={MAX_INVITE_CODE_LEN}
          hint={isInviteRequired ? t('auth.inviteRequiredHint') : undefined}
          value={inviteCode}
          onChange={(e) => setInviteCode(e.target.value)}
        />
        <Button type="submit" variant="primary" isLarge isBlock isBusy={isBusy} disabled={!canSubmit}>
          {t('auth.register')}
        </Button>
      </form>
      <p className="hub-login-links">
        <Link to={ROUTES.LOGIN}>{t('auth.haveAccount')}</Link>
      </p>
    </AuthShell>
  )
}
