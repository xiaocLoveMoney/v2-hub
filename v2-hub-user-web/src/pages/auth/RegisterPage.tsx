import { useState, type FormEvent } from 'react'
import { Link, Navigate, useNavigate, useSearchParams } from 'react-router'
import { api, toApiError, type ApiError, type RegisterInput } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { Alert, Button, Field } from '../../components/ui'
import { mutate } from '../../hooks/use-remote'
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
      <AuthShell lead="注册" subtitle="创建账号后即可查看订阅与节点">
        <Alert tone="info">当前不开放注册，请联系管理员开通账号。</Alert>
        <p className="hub-login-links">
          <Link to={ROUTES.LOGIN}>返回登录</Link>
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
    <AuthShell lead="注册" subtitle="创建账号后即可查看订阅与节点">
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <form onSubmit={(e) => void handleSubmit(e)} noValidate>
        <Field
          label="用户名"
          name="username"
          autoComplete="username"
          maxLength={MAX_USERNAME_LEN}
          hint="3–64 位字母、数字或 _ . -"
          error={isUsernameBad ? '只能用 3–64 位字母、数字或 _ . -' : null}
          value={username}
          onChange={(e) => setUsername(e.target.value.trim())}
        />
        <Field
          label="邮箱"
          name="email"
          type="email"
          autoComplete="email"
          maxLength={MAX_EMAIL_LEN}
          hint="用来登录与找回口令"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />
        {site.emailVerifyRequired && <EmailCodeField email={email} value={emailCode} onChange={setEmailCode} send={sendRegisterCode} />}
        <Field
          label="密码"
          name="password"
          type="password"
          autoComplete="new-password"
          maxLength={MAX_CREDENTIAL_LEN}
          hint={`${MIN_CREDENTIAL_LEN}–${MAX_CREDENTIAL_LEN} 个字符`}
          error={isTooShort ? `至少 ${MIN_CREDENTIAL_LEN} 个字符` : null}
          value={credential}
          onChange={(e) => setCredential(e.target.value)}
        />
        <Field
          label="再输一次密码"
          name="repeat"
          type="password"
          autoComplete="new-password"
          maxLength={MAX_CREDENTIAL_LEN}
          error={isMismatch ? '两次输入不一致' : null}
          value={repeat}
          onChange={(e) => setRepeat(e.target.value)}
        />
        <Field
          label={isInviteRequired ? '邀请码' : '邀请码（可选）'}
          name="inviteCode"
          autoComplete="off"
          maxLength={MAX_INVITE_CODE_LEN}
          hint={isInviteRequired ? '当前只能凭邀请码注册' : undefined}
          value={inviteCode}
          onChange={(e) => setInviteCode(e.target.value)}
        />
        <Button type="submit" variant="primary" isLarge isBlock isBusy={isBusy} disabled={!canSubmit}>
          注册
        </Button>
      </form>
      <p className="hub-login-links">
        <Link to={ROUTES.LOGIN}>已有账号，去登录</Link>
      </p>
    </AuthShell>
  )
}
