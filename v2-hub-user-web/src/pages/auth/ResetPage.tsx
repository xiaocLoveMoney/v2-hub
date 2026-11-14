import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router'
import { api, type ApiError } from '../../api/client'
import { useToast } from '../../components/toast-context'
import { Alert, Button, Field } from '../../components/ui'
import { mutate } from '../../hooks/use-remote'
import { useI18n } from '../../i18n/i18n-context'
import { ROUTES } from '../../menu'
import { useSite } from '../../site/site-context'
import { AuthShell } from './AuthShell'
import { EmailCodeField } from './EmailCodeField'
import { EMAIL_CODE_LENGTH, MAX_CREDENTIAL_LEN, MAX_EMAIL_LEN, MIN_CREDENTIAL_LEN, isEmailLike } from './fields'

const sendResetCode = (email: string): Promise<ApiError | null> =>
  mutate(() => api.POST('/api/user/auth/email-code', { body: { email, purpose: 'reset' } }))

/**
 * 找回密码（docs/03 §5.8）：邮箱 + 验证码 + 新密码。成功后全部会话失效，回登录页重新登录；
 * 两步验证不因此关闭（Ruling-23）。邮件不可用时只能找管理员重置。
 */
export function ResetPage() {
  const site = useSite()
  const { t } = useI18n()
  const toast = useToast()
  const navigate = useNavigate()
  const [email, setEmail] = useState('')
  const [emailCode, setEmailCode] = useState('')
  const [credential, setCredential] = useState('')
  const [repeat, setRepeat] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  if (!site.mailEnabled) {
    return (
      <AuthShell lead={t('auth.resetLead')} subtitle={t('auth.resetSubtitle')}>
        <Alert tone="info">{t('auth.resetNoMail')}</Alert>
        <p className="hub-login-links">
          <Link to={ROUTES.LOGIN}>{t('common.backToLogin')}</Link>
        </p>
      </AuthShell>
    )
  }

  const isTooShort = credential !== '' && credential.length < MIN_CREDENTIAL_LEN
  const isMismatch = repeat !== '' && repeat !== credential
  const canSubmit = isEmailLike(email.trim()) && emailCode.length === EMAIL_CODE_LENGTH && credential.length >= MIN_CREDENTIAL_LEN && repeat === credential

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    setIsBusy(true)
    setError(null)
    const next = credential
    const err = await mutate(() => api.POST('/api/user/auth/password/reset', { body: { email: email.trim(), emailCode, newPassword: next } }))
    setIsBusy(false)
    if (err !== null) {
      setError(err.message)
      return
    }
    toast.show(t('auth.resetDone'), 'success')
    void navigate(ROUTES.LOGIN, { replace: true })
  }

  return (
    <AuthShell lead={t('auth.resetLead')} subtitle={t('auth.resetSubtitle')}>
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <form onSubmit={(e) => void handleSubmit(e)} noValidate>
        <Field
          label={t('auth.email')}
          name="email"
          type="email"
          autoComplete="email"
          maxLength={MAX_EMAIL_LEN}
          hint={t('auth.emailResetHint')}
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />
        <EmailCodeField email={email} value={emailCode} onChange={setEmailCode} send={sendResetCode} />
        <Field
          label={t('auth.newPassword')}
          name="password"
          type="password"
          autoComplete="new-password"
          maxLength={MAX_CREDENTIAL_LEN}
          hint={t('auth.newPasswordHint', { min: MIN_CREDENTIAL_LEN, max: MAX_CREDENTIAL_LEN })}
          error={isTooShort ? t('auth.passwordTooShort', { min: MIN_CREDENTIAL_LEN }) : null}
          value={credential}
          onChange={(e) => setCredential(e.target.value)}
        />
        <Field
          label={t('auth.newPasswordRepeat')}
          name="repeat"
          type="password"
          autoComplete="new-password"
          maxLength={MAX_CREDENTIAL_LEN}
          error={isMismatch ? t('auth.passwordMismatch') : null}
          value={repeat}
          onChange={(e) => setRepeat(e.target.value)}
        />
        <Button type="submit" variant="primary" isLarge isBlock isBusy={isBusy} disabled={!canSubmit}>
          {t('auth.resetSubmit')}
        </Button>
      </form>
      <p className="hub-login-links">
        <Link to={ROUTES.LOGIN}>{t('auth.remembered')}</Link>
      </p>
    </AuthShell>
  )
}
