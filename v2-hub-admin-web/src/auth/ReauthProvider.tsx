import { Button, Form, Input, Space, Typography } from 'antd'
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { CODE_RATE_LIMITED, api, csrfHeader, registerReauthHandler, type ApiError } from '../api/client'
import { ErrorAlert } from '../components/ErrorAlert'
import { FormModal } from '../components/FormModal'
import { mutate } from '../hooks/use-remote'
import { MAX_ADMIN_PASSWORD_LEN, TOTP_CODE_LEN, TOTP_CODE_PATTERN } from '../pages/form-limits'
import { useSession } from './session-context'

/** 频控时的提示：不展示后端原文里的剩余时间，统一让对方稍后再试。 */
const RATE_LIMITED_TEXT = '尝试次数过多，请稍后再试'

interface ReauthForm {
  /** 已开启两步验证时填验证码，否则填密码；只留在这次弹窗的表单里，关闭即销毁。 */
  answer: string
}

interface ReauthProviderProps {
  children: ReactNode
}

/**
 * 高危操作的二次验证（Ruling-100）：向 api 客户端登记处理器，接口回 403 reauth_required 时弹出验证框，
 * 通过后由客户端重发原请求。必须放在 SessionProvider 内（按 session.totpEnabled 决定填验证码还是密码）。
 */
export function ReauthProvider({ children }: ReauthProviderProps) {
  const { session } = useSession()
  const [isOpen, setIsOpen] = useState(false)
  const resolverRef = useRef<((isPassed: boolean) => void) | null>(null)

  const settle = useCallback((isPassed: boolean) => {
    const resolve = resolverRef.current
    resolverRef.current = null
    setIsOpen(false)
    resolve?.(isPassed)
  }, [])

  useEffect(() => {
    const unregister = registerReauthHandler(
      () =>
        new Promise<boolean>((resolve) => {
          resolverRef.current = resolve
          setIsOpen(true)
        }),
    )
    return () => {
      unregister()
      // 卸载时还在等验证的请求按取消处理，原 403 交还调用方
      settle(false)
    }
  }, [settle])

  return (
    <>
      {children}
      <FormModal title="验证身份" isOpen={isOpen} onClose={() => settle(false)}>
        {(generation) => (
          <ReauthBody key={generation} isTotp={session?.totpEnabled ?? false} onPassed={() => settle(true)} onCancel={() => settle(false)} />
        )}
      </FormModal>
    </>
  )
}

interface ReauthBodyProps {
  /** 已开启两步验证：填验证码；否则填密码。 */
  isTotp: boolean
  onPassed: () => void
  onCancel: () => void
}

/** 验证框内容：失败（400）就地提示可重试，频控（429）提示稍后再试。 */
function ReauthBody({ isTotp, onPassed, onCancel }: ReauthBodyProps) {
  const [form] = Form.useForm<ReauthForm>()
  const [error, setError] = useState<ApiError | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const handleFinish = async (values: ReauthForm) => {
    setIsBusy(true)
    setError(null)
    const answer = values.answer
    const body = isTotp ? { totpCode: answer } : { password: answer }
    const err = await mutate(() => api.POST('/api/admin/auth/reauth', { params: csrfHeader(), body }))
    setIsBusy(false)
    if (err === null) {
      onPassed()
      return
    }
    // 输错后清空输入框，不让验证码或密码留在表单里
    form.resetFields()
    setError(err.code === CODE_RATE_LIMITED ? { code: err.code, message: RATE_LIMITED_TEXT } : err)
  }

  return (
    <>
      <ErrorAlert error={error} />
      <Typography.Paragraph>
        这是高危操作，需要先验证身份。{isTotp ? '请输入验证器 App 中的 6 位验证码' : '请输入当前登录密码'}，验证通过后会自动继续刚才的操作。
      </Typography.Paragraph>
      <Form<ReauthForm> form={form} layout="vertical" onFinish={handleFinish}>
        {isTotp ? (
          <Form.Item name="answer" label="验证码" rules={[{ required: true, pattern: TOTP_CODE_PATTERN, message: '请输入 6 位数字验证码' }]}>
            <Input placeholder="6 位验证码" inputMode="numeric" maxLength={TOTP_CODE_LEN} autoComplete="one-time-code" autoFocus />
          </Form.Item>
        ) : (
          <Form.Item name="answer" label="当前密码" rules={[{ required: true, message: '请输入密码' }]}>
            <Input.Password maxLength={MAX_ADMIN_PASSWORD_LEN} autoComplete="current-password" autoFocus />
          </Form.Item>
        )}
        <Space>
          <Button onClick={onCancel}>取消</Button>
          <Button type="primary" htmlType="submit" loading={isBusy}>
            验证并继续
          </Button>
        </Space>
      </Form>
    </>
  )
}
