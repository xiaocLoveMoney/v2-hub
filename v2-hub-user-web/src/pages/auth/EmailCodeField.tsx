import { useState } from 'react'
import type { ApiError } from '../../api/client'
import { useToast } from '../../components/toast-context'
import { Button, Field } from '../../components/ui'
import { useCountdown } from '../../hooks/use-countdown'
import { useI18n } from '../../i18n/i18n-context'
import { EMAIL_CODE_LENGTH, digitsOnly, isEmailLike } from './fields'

/** 同一邮箱每分钟只能发一封，按钮倒计时与之对齐。 */
const RESEND_SECONDS = 60

interface EmailCodeFieldProps {
  /** 收验证码的邮箱（由上面的邮箱输入框提供）。 */
  email: string
  value: string
  onChange: (code: string) => void
  /** 请求发码；成功返回 null。 */
  send: (email: string) => Promise<ApiError | null>
}

/** 验证码输入框 + “发送验证码”按钮；发出后 60 秒内不可再点。 */
export function EmailCodeField({ email, value, onChange, send }: EmailCodeFieldProps) {
  const toast = useToast()
  const { t } = useI18n()
  const [left, startCountdown] = useCountdown()
  const [isSending, setIsSending] = useState(false)

  const handleSend = async () => {
    setIsSending(true)
    const err = await send(email.trim())
    setIsSending(false)
    if (err !== null) {
      toast.show(err.message, 'error')
      return
    }
    startCountdown(RESEND_SECONDS)
    toast.show(t('auth.codeSent'), 'success')
  }

  return (
    <div className="hub-code-row">
      <Field
        label={t('auth.emailCode')}
        name="emailCode"
        inputMode="numeric"
        autoComplete="one-time-code"
        maxLength={EMAIL_CODE_LENGTH}
        value={value}
        onChange={(e) => onChange(digitsOnly(e.target.value))}
      />
      <Button variant="outline" isBusy={isSending} disabled={left > 0 || !isEmailLike(email.trim())} onClick={() => void handleSend()}>
        {left > 0 ? t('auth.resendIn', { seconds: left }) : t('auth.sendCode')}
      </Button>
    </div>
  )
}
