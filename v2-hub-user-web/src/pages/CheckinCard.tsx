import { useState } from 'react'
import { api, csrfHeader, toApiError, type CheckinInfo } from '../api/client'
import { useToast } from '../components/toast-context'
import { Alert, Badge, Button, Card } from '../components/ui'
import { formatBytes } from '../format'
import { useRemote } from '../hooks/use-remote'

/** 卡片里显示最近几次签到。 */
const RECENT_LOG_COUNT = 5

const loadCheckin = (signal: AbortSignal) => api.GET('/api/user/checkin', { signal })

interface CheckinCardProps {
  /** 签到成功后通知仪表盘刷新额度。 */
  onRewarded: () => void
}

/** 签到卡（docs/03 §5.11）：每天一次，奖励加到流量额度；不能签到时显示后端给的原因。 */
export function CheckinCard({ onRewarded }: CheckinCardProps) {
  const toast = useToast()
  const checkin = useRemote<CheckinInfo>(loadCheckin)
  const [isBusy, setIsBusy] = useState(false)
  const info = checkin.data

  const handleCheckin = async () => {
    setIsBusy(true)
    try {
      const res = await api.POST('/api/user/checkin', { params: csrfHeader() })
      if (res.error !== undefined || res.data?.data === undefined) {
        toast.show(toApiError(res.error).message, 'error')
        return
      }
      toast.show(`签到成功，获得 ${formatBytes(res.data.data.rewardBytes)}`, 'success')
      checkin.reload()
      onRewarded()
    } catch (err) {
      reportError(err)
      toast.show(toApiError(err).message, 'error')
    } finally {
      setIsBusy(false)
    }
  }

  if (checkin.error !== null) {
    return (
      <Card title="每日签到">
        <Alert tone="plain">{checkin.error.message}</Alert>
      </Card>
    )
  }
  if (info === null || !info.enabled) {
    return null
  }
  return (
    <Card title="每日签到" extra={info.checkedToday ? <Badge tone="success">今日已签到</Badge> : undefined}>
      <p className="hub-card-text">每天签到一次，随机获得一些流量，加到当前套餐的额度里。</p>
      {!info.canCheckin && !info.checkedToday && info.reason !== '' && <Alert tone="info">{info.reason}</Alert>}
      <Button variant="primary" isBusy={isBusy} disabled={!info.canCheckin} onClick={() => void handleCheckin()}>
        {info.checkedToday ? '明天再来' : '签到'}
      </Button>
      {info.logs.length > 0 && (
        <ul className="hub-plain-list">
          {info.logs.slice(0, RECENT_LOG_COUNT).map((log) => (
            <li key={log.date}>
              <span>{log.date}</span>
              <span>+{formatBytes(log.rewardBytes)}</span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  )
}
