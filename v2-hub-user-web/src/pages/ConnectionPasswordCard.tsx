import { useState, type FormEvent } from 'react'
import { api, csrfHeader, type SubscriptionInfo } from '../api/client'
import { useToast } from '../components/toast-context'
import { Alert, Badge, Button, Card, Field } from '../components/ui'
import { mutate, useRemote } from '../hooks/use-remote'
import { useI18n } from '../i18n/i18n-context'

// 写法与 hub-api.yaml 的 ConnectionPasswordInput 一致（docs/03 §5.4 连接密码，Ruling-58）
const CONN_PASSWORD_PATTERN = /^[A-Za-z0-9._~-]{8,64}$/
const MAX_CONN_PASSWORD_LEN = 64
const RESET_TO_RANDOM = ''

const loadSubscription = (signal: AbortSignal) => api.GET('/api/user/subscription', { signal })

/**
 * 连接密码（docs/03 §5.4 连接密码，Ruling-58）：用户自设面板 client 的 password（Trojan、Shadowsocks、TUIC、MASQUE 用），
 * 或恢复为平台随机生成。后端不回传密码本身，这里只显示是否自设。
 */
export function ConnectionPasswordCard() {
  const { t } = useI18n()
  const toast = useToast()
  const sub = useRemote<SubscriptionInfo>(loadSubscription)
  const [value, setValue] = useState('')
  const [repeat, setRepeat] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)
  const isBadFormat = value !== '' && !CONN_PASSWORD_PATTERN.test(value)
  const isMismatch = repeat !== '' && repeat !== value
  const canSubmit = value !== '' && !isBadFormat && repeat === value

  const submit = async (password: string) => {
    setIsBusy(true)
    setError(null)
    const err = await mutate(() => api.PUT('/api/user/subscription/password', { params: csrfHeader(), body: { password } }))
    setIsBusy(false)
    if (err !== null) {
      setError(err.message)
      return
    }
    setValue('')
    setRepeat('')
    toast.show(t(password === RESET_TO_RANDOM ? 'connPassword.resetDone' : 'connPassword.saved'), 'success')
    sub.reload()
  }
  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (canSubmit) {
      void submit(value)
    }
  }

  const hasCustom = sub.data?.hasCustomPassword === true
  const status =
    sub.data === null ? null : (
      <Badge tone={hasCustom ? 'primary' : 'light'}>{t(hasCustom ? 'connPassword.statusCustom' : 'connPassword.statusRandom')}</Badge>
    )
  return (
    <Card title={t('connPassword.title')} extra={status}>
      <p className="hub-field-hint hub-conn-password-intro">{t('connPassword.intro')}</p>
      {sub.error !== null && <Alert tone="plain">{sub.error.message}</Alert>}
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <form onSubmit={handleSubmit} noValidate className="hub-conn-password-form">
        <Field
          label={t('connPassword.new')}
          type="password"
          autoComplete="new-password"
          maxLength={MAX_CONN_PASSWORD_LEN}
          hint={t('connPassword.rule')}
          error={isBadFormat ? t('connPassword.rule') : null}
          value={value}
          onChange={(e) => setValue(e.target.value)}
        />
        <Field
          label={t('connPassword.repeat')}
          type="password"
          autoComplete="new-password"
          maxLength={MAX_CONN_PASSWORD_LEN}
          error={isMismatch ? t('connPassword.mismatch') : null}
          value={repeat}
          onChange={(e) => setRepeat(e.target.value)}
        />
        <div className="hub-actions">
          <Button type="submit" variant="primary" isBusy={isBusy} disabled={!canSubmit}>
            {t('connPassword.save')}
          </Button>
          {hasCustom && (
            <Button variant="outline" disabled={isBusy} onClick={() => void submit(RESET_TO_RANDOM)}>
              {t('connPassword.reset')}
            </Button>
          )}
        </div>
      </form>
      <p className="hub-field-hint">{t('connPassword.note')}</p>
    </Card>
  )
}
