import { useState } from 'react'
import { api, csrfHeader, type InviteInfo } from '../api/client'
import { useToast } from '../components/toast-context'
import { Alert, Button, Card, Confirm, Empty, PageHeader } from '../components/ui'
import { formatTime } from '../format'
import { useCopy } from '../hooks/use-copy'
import { mutate, useRemote } from '../hooks/use-remote'
import { ROUTES } from '../menu'
import { formatMoney } from '../money'
import { useCurrency } from '../site/site-context'
import { INVITE_QUERY_KEY } from './auth/fields'
import { useI18n } from '../i18n/i18n-context'

const UNLIMITED_USES = 0

const loadInvite = (signal: AbortSignal) => api.GET('/api/user/invite', { signal })

/** 把邀请码拼成注册链接；注册页会从查询参数里预填。 */
function inviteLink(code: string): string {
  const url = new URL(ROUTES.REGISTER, globalThis.location.origin)
  url.searchParams.set(INVITE_QUERY_KEY, code)
  return url.toString()
}

/** 邀请注册（docs/03 §5.8）：个人邀请码与链接、已用次数、最近的被邀请人；重置后旧码作废、已用次数不清零。 */
export function InvitePage() {
  const { t, tag, rich } = useI18n()
  const toast = useToast()
  const copy = useCopy()
  const currency = useCurrency()
  const invite = useRemote<InviteInfo>(loadInvite)
  const [isConfirming, setIsConfirming] = useState(false)
  const [isBusy, setIsBusy] = useState(false)
  const info = invite.data

  const handleReset = async () => {
    setIsBusy(true)
    const err = await mutate(() => api.POST('/api/user/invite/reset', { params: csrfHeader() }))
    setIsBusy(false)
    setIsConfirming(false)
    if (err !== null) {
      toast.show(err.message, 'error')
      return
    }
    toast.show(t('invite.resetDone'), 'success')
    invite.reload()
  }

  return (
    <>
      <PageHeader title={t('menu.invite')} />
      {invite.error !== null && <Alert tone="plain">{invite.error.message}</Alert>}
      {info !== null && !info.enabled && (
        <Card>
          <Empty>{t('invite.disabled')}</Empty>
        </Card>
      )}
      {info !== null && info.enabled && (
        <div className="hub-grid-2">
          <Card title={t('invite.myCode')}>
            <p className="hub-card-text">{t('invite.intro')}</p>
            <label className="hub-field-label" htmlFor="invite-code">
              {t('invite.code')}
            </label>
            <div className="hub-copy-row hub-row-gap">
              <input id="invite-code" className="hub-input hub-mono" readOnly value={info.code} />
              <Button variant="outline" onClick={() => void copy(info.code, t('invite.codeCopied'))}>
                {t('common.copy')}
              </Button>
            </div>
            <label className="hub-field-label" htmlFor="invite-link">
              {t('invite.link')}
            </label>
            <div className="hub-copy-row hub-row-gap">
              <input id="invite-link" className="hub-input" readOnly value={inviteLink(info.code)} />
              <Button variant="outline" onClick={() => void copy(inviteLink(info.code), t('invite.linkCopied'))}>
                {t('common.copy')}
              </Button>
            </div>
            <p className="hub-card-text">
              {t('invite.usedBefore')}
              <strong>{info.usedCount}</strong>
              {info.maxUses === UNLIMITED_USES ? t('invite.usedAfterUnlimited') : t('invite.usedAfterMax', { max: info.maxUses })}
            </p>
            <Button variant="outline" onClick={() => setIsConfirming(true)}>
              {t('invite.reset')}
            </Button>
          </Card>
          <Card title={t('invite.invitees')}>
            <div className="hub-table-wrap">
              <table className="hub-table">
                <thead>
                  <tr>
                    <th>{t('invite.colUsername')}</th>
                    <th>{t('invite.colRegistered')}</th>
                  </tr>
                </thead>
                <tbody>
                  {info.invitees.map((u) => (
                    <tr key={`${u.username}-${u.createdAt}`}>
                      <td>{u.username}</td>
                      <td>{formatTime(u.createdAt, tag)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {info.invitees.length === 0 && <Empty>{t('invite.noInvitees')}</Empty>}
          </Card>
        </div>
      )}
      {info !== null && (info.rebatePercent ?? 0) > 0 && (
        <Card title={t('invite.rebateTitle')}>
          <p className="hub-card-text">
            {rich('invite.rebateRule', { percent: <strong>{info.rebatePercent}</strong> })}{' '}
            {rich('invite.rebateTotal', { amount: <strong>{formatMoney(info.rebateTotal ?? 0, currency, tag)}</strong> })}
          </p>
          <div className="hub-table-wrap">
            <table className="hub-table">
              <thead>
                <tr>
                  <th>{t('invite.colInvitee')}</th>
                  <th>{t('invite.colRebate')}</th>
                  <th>{t('invite.colTime')}</th>
                </tr>
              </thead>
              <tbody>
                {(info.rebates ?? []).map((r) => (
                  <tr key={`${r.username}-${r.createdAt}-${r.amount}`}>
                    <td>{r.username}</td>
                    <td>{formatMoney(r.amount, currency, tag)}</td>
                    <td>{formatTime(r.createdAt, tag)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {(info.rebates ?? []).length === 0 && <Empty>{t('invite.noRebates')}</Empty>}
        </Card>
      )}
      <Confirm title={t('invite.reset')} isOpen={isConfirming} isBusy={isBusy} confirmText={t('sub.resetConfirm')} onConfirm={() => void handleReset()} onClose={() => setIsConfirming(false)}>
        {t('invite.resetConfirmBody')}
      </Confirm>
    </>
  )
}
