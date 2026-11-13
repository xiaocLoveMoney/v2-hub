import { QRCodeSVG } from 'qrcode.react'
import { useState, type FormEvent } from 'react'
import { api, csrfHeader, type LoginLog, type TotpSetup } from '../api/client'
import { useSession } from '../auth/session-context'
import { useToast } from '../components/toast-context'
import { Alert, Badge, Button, Card, Empty, Field, Modal, PageHeader } from '../components/ui'
import { formatTime } from '../format'
import { mutate, useRemote } from '../hooks/use-remote'
import { DeleteAccountCard, EmailCard } from './AccountEmailCards'
import { useI18n } from '../i18n/i18n-context'

const MIN_CREDENTIAL_LEN = 8
const MAX_CREDENTIAL_LEN = 128
const TOTP_LENGTH = 6
const QR_SIZE = 180

const loadLoginLogs = (signal: AbortSignal) => api.GET('/api/user/login-logs', { signal })

function digitsOnly(v: string): string {
  return v.replace(/\D/g, '')
}

/** 改口令：须提交旧口令，成功后其它设备上的登录会失效（docs/03 §5.3）。 */
function CredentialCard() {
  const { t } = useI18n()
  const toast = useToast()
  const [oldValue, setOldValue] = useState('')
  const [newValue, setNewValue] = useState('')
  const [repeat, setRepeat] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)
  const tooShort = newValue !== '' && newValue.length < MIN_CREDENTIAL_LEN
  const mismatch = repeat !== '' && repeat !== newValue

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    setIsBusy(true)
    setError(null)
    const next = newValue
    const err = await mutate(() => api.PUT('/api/user/auth/password', { params: csrfHeader(), body: { oldPassword: oldValue, newPassword: next } }))
    setIsBusy(false)
    if (err !== null) {
      setError(err.message)
      return
    }
    setOldValue('')
    setNewValue('')
    setRepeat('')
    toast.show(t('account.credentialChanged'), 'success')
  }

  return (
    <Card title={t('account.credentialTitle')}>
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <form onSubmit={(e) => void handleSubmit(e)} noValidate>
        <Field label={t('account.currentCredential')} type="password" autoComplete="current-password" maxLength={MAX_CREDENTIAL_LEN} value={oldValue} onChange={(e) => setOldValue(e.target.value)} />
        <Field
          label={t('account.newCredential')}
          type="password"
          autoComplete="new-password"
          maxLength={MAX_CREDENTIAL_LEN}
          hint={t('auth.passwordHint', { min: MIN_CREDENTIAL_LEN, max: MAX_CREDENTIAL_LEN })}
          error={tooShort ? t('auth.passwordTooShort', { min: MIN_CREDENTIAL_LEN }) : null}
          value={newValue}
          onChange={(e) => setNewValue(e.target.value)}
        />
        <Field
          label={t('account.newCredentialRepeat')}
          type="password"
          autoComplete="new-password"
          maxLength={MAX_CREDENTIAL_LEN}
          error={mismatch ? t('auth.passwordMismatch') : null}
          value={repeat}
          onChange={(e) => setRepeat(e.target.value)}
        />
        <Button type="submit" variant="primary" isBusy={isBusy} disabled={oldValue === '' || newValue.length < MIN_CREDENTIAL_LEN || repeat !== newValue}>
          {t('common.save')}
        </Button>
      </form>
    </Card>
  )
}

/** 两步验证：开启分两步（生成密钥 → 输入验证码确认），关闭须提交当前验证码（docs/03 §5.3）。 */
function TotpCard() {
  const { t } = useI18n()
  const toast = useToast()
  const { session, refresh } = useSession()
  const isEnabled = session?.totpEnabled === true
  const [setup, setSetup] = useState<TotpSetup | null>(null)
  const [isDisabling, setIsDisabling] = useState(false)
  const [code, setCode] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const close = () => {
    setSetup(null)
    setIsDisabling(false)
    setCode('')
    setError(null)
  }

  const startSetup = async () => {
    setIsBusy(true)
    let next: TotpSetup | null = null
    const err = await mutate(async () => {
      const res = await api.POST('/api/user/auth/totp/setup', { params: csrfHeader() })
      next = res.data?.data ?? null
      return res
    })
    setIsBusy(false)
    if (err !== null || next === null) {
      toast.show(err?.message ?? t('account.totpSetupFailed'), 'error')
      return
    }
    setSetup(next)
  }

  const submitCode = async () => {
    setIsBusy(true)
    setError(null)
    const path = isDisabling ? '/api/user/auth/totp/disable' : '/api/user/auth/totp/enable'
    const err = await mutate(() => api.POST(path, { params: csrfHeader(), body: { code } }))
    setIsBusy(false)
    if (err !== null) {
      setError(err.message)
      return
    }
    toast.show(t(isDisabling ? 'account.totpOff' : 'account.totpOn'), 'success')
    close()
    await refresh()
  }

  return (
    <Card title={t('account.totpTitle')} extra={<Badge tone={isEnabled ? 'success' : undefined}>{t(isEnabled ? 'wallet.on' : 'wallet.off')}</Badge>}>
      <p style={{ marginBottom: 16 }}>{t('account.totpIntro')}</p>
      {isEnabled ? (
        <Button variant="outline" onClick={() => setIsDisabling(true)}>
          {t('account.totpDisable')}
        </Button>
      ) : (
        <Button variant="primary" isBusy={isBusy} onClick={() => void startSetup()}>
          {t('account.totpEnable')}
        </Button>
      )}
      <Modal
        title={t(isDisabling ? 'account.totpDisable' : 'account.totpEnable')}
        isOpen={setup !== null || isDisabling}
        onClose={close}
        footer={
          <>
            <Button variant="outline" onClick={close}>
              {t('common.cancel')}
            </Button>
            <Button variant={isDisabling ? 'danger' : 'primary'} isBusy={isBusy} disabled={code.length !== TOTP_LENGTH} onClick={() => void submitCode()}>
              {t(isDisabling ? 'account.totpDisableShort' : 'account.totpEnableConfirm')}
            </Button>
          </>
        }
      >
        {error !== null && <Alert tone="plain">{error}</Alert>}
        {setup !== null && (
          <>
            <p style={{ marginBottom: 12 }}>{t('account.totpScan')}</p>
            <div style={{ display: 'grid', placeItems: 'center', marginBottom: 12 }}>
              <QRCodeSVG value={setup.otpauthUri} size={QR_SIZE} />
            </div>
            <p className="hub-mono" style={{ marginBottom: 16, textAlign: 'center' }}>
              {setup.secret}
            </p>
          </>
        )}
        <Field
          label={t('account.totpCode')}
          inputMode="numeric"
          autoComplete="one-time-code"
          maxLength={TOTP_LENGTH}
          hint={t('account.totpCodeHint')}
          value={code}
          onChange={(e) => setCode(digitsOnly(e.target.value))}
        />
      </Modal>
    </Card>
  )
}

function LoginLogsCard() {
  const { t, tag } = useI18n()
  const logs = useRemote<LoginLog[]>(loadLoginLogs)
  return (
    <Card title={t('account.loginLogs')}>
      {logs.error !== null && <Alert tone="plain">{logs.error.message}</Alert>}
      <div className="hub-table-wrap">
        <table className="hub-table">
          <thead>
            <tr>
              <th>{t('account.colTime')}</th>
              <th>IP</th>
              <th>{t('account.colResult')}</th>
            </tr>
          </thead>
          <tbody>
            {(logs.data ?? []).map((l) => (
              <tr key={`${l.createdAt}-${l.clientIp}-${String(l.isSuccess)}`}>
                <td>{formatTime(l.createdAt, tag)}</td>
                <td>{l.clientIp || t('common.dash')}</td>
                <td>
                  <Badge tone={l.isSuccess ? 'success' : 'danger'}>{t(l.isSuccess ? 'account.success' : 'account.failure')}</Badge>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {logs.data !== null && logs.data.length === 0 && <Empty>{t('account.noLogs')}</Empty>}
    </Card>
  )
}

/** 我的账号（docs/06 §5）：改口令、邮箱、注销账号、两步验证、最近登录。 */
export function AccountPage() {
  const { t } = useI18n()
  return (
    <>
      <PageHeader title={t('menu.account')} />
      <div className="hub-grid-2">
        <div>
          <CredentialCard />
          <EmailCard />
          <DeleteAccountCard />
        </div>
        <div>
          <TotpCard />
          <LoginLogsCard />
        </div>
      </div>
    </>
  )
}
