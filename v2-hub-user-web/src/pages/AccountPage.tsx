import { QRCodeSVG } from 'qrcode.react'
import { useState, type FormEvent } from 'react'
import { api, csrfHeader, type LoginLog, type TotpSetup } from '../api/client'
import { useSession } from '../auth/session-context'
import { useToast } from '../components/toast-context'
import { Alert, Badge, Button, Card, Empty, Field, Modal, PageHeader } from '../components/ui'
import { formatTime } from '../format'
import { mutate, useRemote } from '../hooks/use-remote'
import { DeleteAccountCard, EmailCard } from './AccountEmailCards'

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
    toast.show('口令已修改，其它设备需要重新登录', 'success')
  }

  return (
    <Card title="修改登录口令">
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <form onSubmit={(e) => void handleSubmit(e)} noValidate>
        <Field label="当前口令" type="password" autoComplete="current-password" maxLength={MAX_CREDENTIAL_LEN} value={oldValue} onChange={(e) => setOldValue(e.target.value)} />
        <Field
          label="新口令"
          type="password"
          autoComplete="new-password"
          maxLength={MAX_CREDENTIAL_LEN}
          hint={`${MIN_CREDENTIAL_LEN}–${MAX_CREDENTIAL_LEN} 个字符`}
          error={tooShort ? `至少 ${MIN_CREDENTIAL_LEN} 个字符` : null}
          value={newValue}
          onChange={(e) => setNewValue(e.target.value)}
        />
        <Field
          label="再输一次新口令"
          type="password"
          autoComplete="new-password"
          maxLength={MAX_CREDENTIAL_LEN}
          error={mismatch ? '两次输入不一致' : null}
          value={repeat}
          onChange={(e) => setRepeat(e.target.value)}
        />
        <Button type="submit" variant="primary" isBusy={isBusy} disabled={oldValue === '' || newValue.length < MIN_CREDENTIAL_LEN || repeat !== newValue}>
          保存
        </Button>
      </form>
    </Card>
  )
}

/** 两步验证：开启分两步（生成密钥 → 输入验证码确认），关闭须提交当前验证码（docs/03 §5.3）。 */
function TotpCard() {
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
      toast.show(err?.message ?? '生成密钥失败', 'error')
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
    toast.show(isDisabling ? '已关闭两步验证' : '已开启两步验证', 'success')
    close()
    await refresh()
  }

  return (
    <Card title="两步验证" extra={<Badge tone={isEnabled ? 'success' : undefined}>{isEnabled ? '已开启' : '未开启'}</Badge>}>
      <p style={{ marginBottom: 16 }}>开启后，登录时除了口令还要输入验证器 App（如 Google Authenticator、1Password）里的 6 位数字。</p>
      {isEnabled ? (
        <Button variant="outline" onClick={() => setIsDisabling(true)}>
          关闭两步验证
        </Button>
      ) : (
        <Button variant="primary" isBusy={isBusy} onClick={() => void startSetup()}>
          开启两步验证
        </Button>
      )}
      <Modal
        title={isDisabling ? '关闭两步验证' : '开启两步验证'}
        isOpen={setup !== null || isDisabling}
        onClose={close}
        footer={
          <>
            <Button variant="outline" onClick={close}>
              取消
            </Button>
            <Button variant={isDisabling ? 'danger' : 'primary'} isBusy={isBusy} disabled={code.length !== TOTP_LENGTH} onClick={() => void submitCode()}>
              {isDisabling ? '关闭' : '确认开启'}
            </Button>
          </>
        }
      >
        {error !== null && <Alert tone="plain">{error}</Alert>}
        {setup !== null && (
          <>
            <p style={{ marginBottom: 12 }}>用验证器 App 扫描二维码，或手动输入密钥：</p>
            <div style={{ display: 'grid', placeItems: 'center', marginBottom: 12 }}>
              <QRCodeSVG value={setup.otpauthUri} size={QR_SIZE} />
            </div>
            <p className="hub-mono" style={{ marginBottom: 16, textAlign: 'center' }}>
              {setup.secret}
            </p>
          </>
        )}
        <Field
          label="验证码"
          inputMode="numeric"
          autoComplete="one-time-code"
          maxLength={TOTP_LENGTH}
          hint="输入验证器 App 当前显示的 6 位数字"
          value={code}
          onChange={(e) => setCode(digitsOnly(e.target.value))}
        />
      </Modal>
    </Card>
  )
}

function LoginLogsCard() {
  const logs = useRemote<LoginLog[]>(loadLoginLogs)
  return (
    <Card title="最近登录">
      {logs.error !== null && <Alert tone="plain">{logs.error.message}</Alert>}
      <div className="hub-table-wrap">
        <table className="hub-table">
          <thead>
            <tr>
              <th>时间</th>
              <th>IP</th>
              <th>结果</th>
            </tr>
          </thead>
          <tbody>
            {(logs.data ?? []).map((l) => (
              <tr key={`${l.createdAt}-${l.clientIp}-${String(l.isSuccess)}`}>
                <td>{formatTime(l.createdAt)}</td>
                <td>{l.clientIp || '—'}</td>
                <td>
                  <Badge tone={l.isSuccess ? 'success' : 'danger'}>{l.isSuccess ? '成功' : '失败'}</Badge>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {logs.data !== null && logs.data.length === 0 && <Empty>暂无记录</Empty>}
    </Card>
  )
}

/** 我的账号（docs/06 §5）：改口令、邮箱、注销账号、两步验证、最近登录。 */
export function AccountPage() {
  return (
    <>
      <PageHeader title="我的账号" />
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
