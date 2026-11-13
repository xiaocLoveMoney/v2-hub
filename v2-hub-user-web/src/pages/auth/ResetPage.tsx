import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router'
import { api, type ApiError } from '../../api/client'
import { useToast } from '../../components/toast-context'
import { Alert, Button, Field } from '../../components/ui'
import { mutate } from '../../hooks/use-remote'
import { ROUTES } from '../../menu'
import { useSite } from '../../site/site-context'
import { AuthShell } from './AuthShell'
import { EmailCodeField } from './EmailCodeField'
import { EMAIL_CODE_LENGTH, MAX_CREDENTIAL_LEN, MAX_EMAIL_LEN, MIN_CREDENTIAL_LEN, isEmailLike } from './fields'

const sendResetCode = (email: string): Promise<ApiError | null> =>
  mutate(() => api.POST('/api/user/auth/email-code', { body: { email, purpose: 'reset' } }))

/**
 * 找回口令（docs/03 §5.8）：邮箱 + 验证码 + 新口令。成功后全部会话失效，回登录页重新登录；
 * 两步验证不因此关闭（Ruling-23）。邮件不可用时只能找管理员重置。
 */
export function ResetPage() {
  const site = useSite()
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
      <AuthShell lead="找回口令" subtitle="凭绑定的邮箱重设登录口令">
        <Alert tone="info">本站没有开通邮件，无法自助找回，请联系管理员重置口令。</Alert>
        <p className="hub-login-links">
          <Link to={ROUTES.LOGIN}>返回登录</Link>
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
    toast.show('口令已重设，请用新口令登录', 'success')
    void navigate(ROUTES.LOGIN, { replace: true })
  }

  return (
    <AuthShell lead="找回口令" subtitle="凭绑定的邮箱重设登录口令">
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <form onSubmit={(e) => void handleSubmit(e)} noValidate>
        <Field
          label="邮箱"
          name="email"
          type="email"
          autoComplete="email"
          maxLength={MAX_EMAIL_LEN}
          hint="注册或绑定时用的邮箱"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />
        <EmailCodeField email={email} value={emailCode} onChange={setEmailCode} send={sendResetCode} />
        <Field
          label="新密码"
          name="password"
          type="password"
          autoComplete="new-password"
          maxLength={MAX_CREDENTIAL_LEN}
          hint={`${MIN_CREDENTIAL_LEN}–${MAX_CREDENTIAL_LEN} 个字符；开启了两步验证的账号登录时仍要验证码`}
          error={isTooShort ? `至少 ${MIN_CREDENTIAL_LEN} 个字符` : null}
          value={credential}
          onChange={(e) => setCredential(e.target.value)}
        />
        <Field
          label="再输一次新密码"
          name="repeat"
          type="password"
          autoComplete="new-password"
          maxLength={MAX_CREDENTIAL_LEN}
          error={isMismatch ? '两次输入不一致' : null}
          value={repeat}
          onChange={(e) => setRepeat(e.target.value)}
        />
        <Button type="submit" variant="primary" isLarge isBlock isBusy={isBusy} disabled={!canSubmit}>
          重设口令
        </Button>
      </form>
      <p className="hub-login-links">
        <Link to={ROUTES.LOGIN}>想起来了，去登录</Link>
      </p>
    </AuthShell>
  )
}
