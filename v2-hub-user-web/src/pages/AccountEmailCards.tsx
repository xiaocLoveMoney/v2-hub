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
import { useI18n } from '../i18n/i18n-context'

const sendBindCode = (email: string): Promise<ApiError | null> =>
  mutate(() => api.POST('/api/user/account/email-code', { params: csrfHeader(), body: { email } }))

/** 绑定 / 换绑邮箱（docs/03 §5.8）：当前密码 + 新邮箱收到的验证码。邮件不可用时只能找管理员改。 */
export function EmailCard() {
  const { t } = useI18n()
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
      toast.show(t('account.emailBound'), 'success')
    } catch (err) {
      reportError(err)
      setError(toApiError(err).message)
    } finally {
      setIsBusy(false)
    }
  }

  return (
    <Card title={t('account.emailTitle')} extra={boundEmail === null ? <Badge>{t('account.emailUnbound')}</Badge> : <Badge tone={isVerified ? 'success' : 'warning'}>{t(isVerified ? 'account.emailVerified' : 'account.emailUnverified')}</Badge>}>
      <p className="hub-card-text">
        {boundEmail === null ? t('account.emailIntro') : t('account.emailCurrent', { email: boundEmail })}
      </p>
      {!site.mailEnabled && <Alert tone="info">{t('account.emailNoMail')}</Alert>}
      {site.mailEnabled && (
        <>
          {error !== null && <Alert tone="plain">{error}</Alert>}
          <form onSubmit={(e) => void handleSubmit(e)} noValidate>
            <Field
              label={t(boundEmail === null ? 'auth.email' : 'account.newEmail')}
              type="email"
              autoComplete="email"
              maxLength={MAX_EMAIL_LEN}
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
            <EmailCodeField email={email} value={emailCode} onChange={setEmailCode} send={sendBindCode} />
            <Field
              label={t('account.confirmCredential')}
              type="password"
              autoComplete="current-password"
              maxLength={MAX_CREDENTIAL_LEN}
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
            />
            <Button type="submit" variant="primary" isBusy={isBusy} disabled={!isEmailLike(email.trim()) || emailCode.length !== EMAIL_CODE_LENGTH || current === ''}>
              {t(boundEmail === null ? 'account.bind' : 'account.rebind')}
            </Button>
          </form>
        </>
      )}
    </Card>
  )
}

/** 注销账号（docs/03 §5.8）：须提交当前密码，开了两步验证的另须验证码；按删除用户处理，不可恢复。 */
export function DeleteAccountCard() {
  const { t } = useI18n()
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
    toast.show(t('account.deleted'), 'success')
    // 服务端已清掉会话，这里只清本地状态
    clear()
    void navigate(ROUTES.LOGIN, { replace: true })
  }

  return (
    <Card title={t('account.deleteTitle')}>
      <p className="hub-card-text">{t('account.deleteIntro')}</p>
      <Button variant="danger" onClick={() => setIsOpen(true)}>
        {t('account.deleteTitle')}
      </Button>
      <Modal
        title={t('account.deleteConfirmTitle')}
        isOpen={isOpen}
        onClose={close}
        footer={
          <>
            <Button variant="outline" onClick={close}>
              {t('common.cancel')}
            </Button>
            <Button variant="danger" isBusy={isBusy} disabled={current === '' || (needsTotp && totpCode.length !== TOTP_LENGTH)} onClick={() => void handleDelete()}>
              {t('account.deleteForever')}
            </Button>
          </>
        }
      >
        {error !== null && <Alert tone="plain">{error}</Alert>}
        <Alert tone="danger">{t('account.deleteWarning')}</Alert>
        <Field label={t('account.currentCredential')} type="password" autoComplete="current-password" maxLength={MAX_CREDENTIAL_LEN} value={current} onChange={(e) => setCurrent(e.target.value)} />
        {needsTotp && (
          <Field
            label={t('auth.totp')}
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
