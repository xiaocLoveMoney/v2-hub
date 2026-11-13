import { useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router'
import { api, csrfHeader, toApiError, type ApiError } from '../api/client'
import { useSession } from '../auth/session-context'
import { useToast } from '../components/toast-context'
import { Alert, Badge, Button, Card, Field, Modal } from '../components/ui'
import { mutate } from '../hooks/use-remote'
import { ROUTES } from '../menu'
import { useSite } from '../site/site-context'
import { EmailCodeField } from './auth/EmailCodeField'
import { EMAIL_CODE_LENGTH, MAX_CREDENTIAL_LEN, MAX_EMAIL_LEN, TOTP_LENGTH, digitsOnly, isEmailLike } from './auth/fields'

const sendBindCode = (email: string): Promise<ApiError | null> =>
  mutate(() => api.POST('/api/user/account/email-code', { params: csrfHeader(), body: { email } }))

/** 绑定 / 换绑邮箱（docs/03 §5.8）：当前口令 + 新邮箱收到的验证码。邮件不可用时只能找管理员改。 */
export function EmailCard() {
  const site = useSite()
  const toast = useToast()
  const { session, adopt } = useSession()
  const [email, setEmail] = useState('')
  const [emailCode, setEmailCode] = useState('')
  const [current, setCurrent] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)
  const boundEmail = session?.user.email ?? null
  const isVerified = session?.user.isEmailVerified === true

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    setIsBusy(true)
    setError(null)
    const pw = current
    try {
      const res = await api.PUT('/api/user/account/email', { params: csrfHeader(), body: { currentPassword: pw, email: email.trim(), emailCode } })
      if (res.error !== undefined || res.data?.data === undefined) {
        setError(toApiError(res.error).message)
        return
      }
      adopt(res.data.data)
      setEmail('')
      setEmailCode('')
      setCurrent('')
      toast.show('邮箱已绑定', 'success')
    } catch (err) {
      reportError(err)
      setError(toApiError(err).message)
    } finally {
      setIsBusy(false)
    }
  }

  return (
    <Card title="邮箱" extra={boundEmail === null ? <Badge>未绑定</Badge> : <Badge tone={isVerified ? 'success' : 'warning'}>{isVerified ? '已验证' : '未验证'}</Badge>}>
      <p className="hub-card-text">
        {boundEmail === null ? '绑定邮箱后可以用邮箱登录、找回口令。' : `当前邮箱：${boundEmail}`}
      </p>
      {!site.mailEnabled && <Alert tone="info">本站没有开通邮件，绑定或更换邮箱请联系管理员。</Alert>}
      {site.mailEnabled && (
        <>
          {error !== null && <Alert tone="plain">{error}</Alert>}
          <form onSubmit={(e) => void handleSubmit(e)} noValidate>
            <Field
              label={boundEmail === null ? '邮箱' : '新邮箱'}
              type="email"
              autoComplete="email"
              maxLength={MAX_EMAIL_LEN}
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
            <EmailCodeField email={email} value={emailCode} onChange={setEmailCode} send={sendBindCode} />
            <Field
              label="当前口令（确认是本人）"
              type="password"
              autoComplete="current-password"
              maxLength={MAX_CREDENTIAL_LEN}
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
            />
            <Button type="submit" variant="primary" isBusy={isBusy} disabled={!isEmailLike(email.trim()) || emailCode.length !== EMAIL_CODE_LENGTH || current === ''}>
              {boundEmail === null ? '绑定' : '换绑'}
            </Button>
          </form>
        </>
      )}
    </Card>
  )
}

/** 注销账号（docs/03 §5.8）：须提交当前口令，开了两步验证的另须验证码；按删除用户处理，不可恢复。 */
export function DeleteAccountCard() {
  const toast = useToast()
  const navigate = useNavigate()
  const { session, clear } = useSession()
  const [isOpen, setIsOpen] = useState(false)
  const [current, setCurrent] = useState('')
  const [totpCode, setTotpCode] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)
  const needsTotp = session?.totpEnabled === true

  const close = () => {
    setIsOpen(false)
    setCurrent('')
    setTotpCode('')
    setError(null)
  }

  const handleDelete = async () => {
    setIsBusy(true)
    setError(null)
    const pw = current
    const err = await mutate(() =>
      api.POST('/api/user/account/delete', { params: csrfHeader(), body: { currentPassword: pw, ...(needsTotp ? { totpCode } : {}) } }),
    )
    setIsBusy(false)
    if (err !== null) {
      setError(err.message)
      return
    }
    toast.show('账号已注销', 'success')
    // 服务端已清掉会话，这里只清本地状态
    clear()
    void navigate(ROUTES.LOGIN, { replace: true })
  }

  return (
    <Card title="注销账号">
      <p className="hub-card-text">注销后账号、套餐、订阅地址与工单全部删除，各节点上的连接凭据同时失效，无法恢复。</p>
      <Button variant="danger" onClick={() => setIsOpen(true)}>
        注销账号
      </Button>
      <Modal
        title="确认注销账号"
        isOpen={isOpen}
        onClose={close}
        footer={
          <>
            <Button variant="outline" onClick={close}>
              取消
            </Button>
            <Button variant="danger" isBusy={isBusy} disabled={current === '' || (needsTotp && totpCode.length !== TOTP_LENGTH)} onClick={() => void handleDelete()}>
              永久注销
            </Button>
          </>
        }
      >
        {error !== null && <Alert tone="plain">{error}</Alert>}
        <Alert tone="danger">此操作不可恢复。</Alert>
        <Field label="当前口令" type="password" autoComplete="current-password" maxLength={MAX_CREDENTIAL_LEN} value={current} onChange={(e) => setCurrent(e.target.value)} />
        {needsTotp && (
          <Field
            label="两步验证码"
            inputMode="numeric"
            autoComplete="one-time-code"
            maxLength={TOTP_LENGTH}
            value={totpCode}
            onChange={(e) => setTotpCode(digitsOnly(e.target.value))}
          />
        )}
      </Modal>
    </Card>
  )
}
