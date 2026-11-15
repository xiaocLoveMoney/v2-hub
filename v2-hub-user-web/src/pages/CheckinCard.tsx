import { useState } from 'react'
import { api, csrfHeader, type CheckinInfo } from '../api/client'
import { useToast } from '../components/toast-context'
import { Alert, Badge, Button, Card } from '../components/ui'
import { formatBytes } from '../format'
import { request, useRemote } from '../hooks/use-remote'
import { useI18n } from '../i18n/i18n-context'

/** 卡片里显示最近几次签到。 */
const RECENT_LOG_COUNT = 5

const loadCheckin = (signal: AbortSignal) => api.GET('/api/user/checkin', { signal })

interface CheckinCardProps {
  /** 签到成功后通知仪表盘刷新额度。 */
  onRewarded: () => void
}

/** 签到卡（docs/03 §5.11）：每天一次，奖励加到流量额度；不能签到时显示后端给的原因。 */
export function CheckinCard({ onRewarded }: CheckinCardProps) {
  const { t } = useI18n()
  const toast = useToast()
  const checkin = useRemote<CheckinInfo>(loadCheckin)
  const [isBusy, setIsBusy] = useState(false)
  const info = checkin.data

  const handleCheckin = async () => {
    setIsBusy(true)
    try {
      const result = await request(() => api.POST('/api/user/checkin', { params: csrfHeader() }))
      if (result.error !== null) {
        toast.show(result.error.message, 'error')
        return
      }
      toast.show(t('checkin.done', { amount: formatBytes(result.data.rewardBytes) }), 'success')
      checkin.reload()
      onRewarded()
    } finally {
      setIsBusy(false)
    }
  }

  if (checkin.error !== null) {
    return (
      <Card title={t('checkin.title')}>
        <Alert tone="plain">{checkin.error.message}</Alert>
      </Card>
    )
  }
  if (info === null || !info.enabled) {
    return null
  }
  return (
    <Card title={t('checkin.title')} extra={info.checkedToday ? <Badge tone="success">{t('checkin.checkedToday')}</Badge> : undefined}>
      <p className="hub-card-text">{t('checkin.rule')}</p>
      {!info.canCheckin && !info.checkedToday && info.reason !== '' && <Alert tone="info">{info.reason}</Alert>}
      <Button variant="primary" isBusy={isBusy} disabled={!info.canCheckin} onClick={() => void handleCheckin()}>
        {t(info.checkedToday ? 'checkin.tomorrow' : 'checkin.action')}
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
