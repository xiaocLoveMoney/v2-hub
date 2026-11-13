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
    toast.show('邀请码已重置，旧的邀请码与链接已作废', 'success')
    invite.reload()
  }

  return (
    <>
      <PageHeader title="邀请注册" />
      {invite.error !== null && <Alert tone="plain">{invite.error.message}</Alert>}
      {info !== null && !info.enabled && (
        <Card>
          <Empty>个人邀请码暂未开放</Empty>
        </Card>
      )}
      {info !== null && info.enabled && (
        <div className="hub-grid-2">
          <Card title="我的邀请码">
            <p className="hub-card-text">把邀请码或邀请链接发给朋友，对方注册时填上即可。</p>
            <label className="hub-field-label" htmlFor="invite-code">
              邀请码
            </label>
            <div className="hub-copy-row hub-row-gap">
              <input id="invite-code" className="hub-input hub-mono" readOnly value={info.code} />
              <Button variant="outline" onClick={() => void copy(info.code, '邀请码已复制')}>
                复制
              </Button>
            </div>
            <label className="hub-field-label" htmlFor="invite-link">
              邀请链接
            </label>
            <div className="hub-copy-row hub-row-gap">
              <input id="invite-link" className="hub-input" readOnly value={inviteLink(info.code)} />
              <Button variant="outline" onClick={() => void copy(inviteLink(info.code), '邀请链接已复制')}>
                复制
              </Button>
            </div>
            <p className="hub-card-text">
              已使用 <strong>{info.usedCount}</strong> 次，{info.maxUses === UNLIMITED_USES ? '不限次数' : `最多 ${info.maxUses} 次`}
            </p>
            <Button variant="outline" onClick={() => setIsConfirming(true)}>
              重置邀请码
            </Button>
          </Card>
          <Card title="我邀请的人">
            <div className="hub-table-wrap">
              <table className="hub-table">
                <thead>
                  <tr>
                    <th>用户名</th>
                    <th>注册时间</th>
                  </tr>
                </thead>
                <tbody>
                  {info.invitees.map((u) => (
                    <tr key={`${u.username}-${u.createdAt}`}>
                      <td>{u.username}</td>
                      <td>{formatTime(u.createdAt)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {info.invitees.length === 0 && <Empty>还没有人用你的邀请码注册</Empty>}
          </Card>
        </div>
      )}
      {info !== null && (info.rebatePercent ?? 0) > 0 && (
        <Card title="邀请返利">
          <p className="hub-card-text">
            你邀请的人每次消费，实付金额的 <strong>{info.rebatePercent}%</strong> 会记到你的余额里（以站点当前的返利规则为准）。返利只能在本站消费，不能提现。
            累计已获得 <strong>{formatMoney(info.rebateTotal ?? 0, currency)}</strong>。
          </p>
          <div className="hub-table-wrap">
            <table className="hub-table">
              <thead>
                <tr>
                  <th>被邀请人</th>
                  <th>返利</th>
                  <th>时间</th>
                </tr>
              </thead>
              <tbody>
                {(info.rebates ?? []).map((r) => (
                  <tr key={`${r.username}-${r.createdAt}-${r.amount}`}>
                    <td>{r.username}</td>
                    <td>{formatMoney(r.amount, currency)}</td>
                    <td>{formatTime(r.createdAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {(info.rebates ?? []).length === 0 && <Empty>还没有返利记录</Empty>}
        </Card>
      )}
      <Confirm title="重置邀请码" isOpen={isConfirming} isBusy={isBusy} confirmText="重置" onConfirm={() => void handleReset()} onClose={() => setIsConfirming(false)}>
        重置后旧的邀请码与邀请链接立即作废，已使用的次数不会清零。确定要重置吗？
      </Confirm>
    </>
  )
}
