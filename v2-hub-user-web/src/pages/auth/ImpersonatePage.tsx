import { useEffect, useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router'
import { api, toApiError } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { Alert } from '../../components/ui'
import { useI18n } from '../../i18n/i18n-context'
import { ROUTES } from '../../menu'
import { AuthShell } from './AuthShell'

/** 管理端打开本页时把票据放在地址的 # 片段里（不会随请求发到服务器日志），形如 #ticket=xxx（docs/03 §5.17）。 */
const TICKET_PARAM = 'ticket'

function ticketFromHash(hash: string): string {
  return new URLSearchParams(hash.startsWith('#') ? hash.slice(1) : hash).get(TICKET_PARAM) ?? ''
}

/**
 * 切换为用户身份的落地页：用票据换取只读的代入会话，成功后立即用 replace 跳到仪表盘，
 * 票据因此不留在地址栏与历史记录里。票据只在内存里用一次，不写入任何存储。
 */
export function ImpersonatePage() {
  const { adopt } = useSession()
  const navigate = useNavigate()
  const location = useLocation()
  // 进入页面时取一次票据；之后地址变化不再重新兑换
  const [ticket] = useState(() => ticketFromHash(location.hash))
  const { t } = useI18n()
  const [error, setError] = useState<string | null>(() => (ticket === '' ? t('auth.impersonateNoTicket') : null))

  useEffect(() => {
    if (ticket === '') {
      return undefined
    }
    const controller = new AbortController()
    const redeem = async () => {
      try {
        const res = await api.POST('/api/user/auth/impersonate', { body: { ticket }, signal: controller.signal })
        if (controller.signal.aborted) {
          return
        }
        if (res.error !== undefined || res.data?.data === undefined) {
          setError(toApiError(res.error).message)
          return
        }
        adopt(res.data.data)
        void navigate(ROUTES.DASHBOARD, { replace: true })
      } catch (err) {
        if (controller.signal.aborted) {
          return
        }
        reportError(err)
        setError(toApiError(err).message)
      }
    }
    void redeem()
    return () => controller.abort()
  }, [ticket, adopt, navigate])

  return (
    <AuthShell lead={t('auth.impersonateLead')} subtitle={t('auth.impersonateSubtitle')}>
      {error === null ? (
        <div className="hub-empty">{t('auth.impersonateVerifying')}</div>
      ) : (
        <>
          <Alert tone="plain">{error}</Alert>
          <p className="hub-login-links">
            <Link to={ROUTES.LOGIN}>{t('common.backToLogin')}</Link>
          </p>
        </>
      )}
    </AuthShell>
  )
}
