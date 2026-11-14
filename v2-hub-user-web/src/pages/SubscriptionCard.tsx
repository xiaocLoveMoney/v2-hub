import { Copy, Eye, EyeOff, QrCode, RefreshCw } from 'lucide-react'
import { QRCodeSVG } from 'qrcode.react'
import { useCallback, useState, type CSSProperties } from 'react'
import { api, csrfHeader, type SubscriptionInfo } from '../api/client'
import { BRAND } from '../app-config'
import { useToast } from '../components/toast-context'
import { Alert, Button, Card, Confirm, Loading, Modal } from '../components/ui'
import { useCopy } from '../hooks/use-copy'
import { mutate, useRemote } from '../hooks/use-remote'
import { useI18n } from '../i18n/i18n-context'
import { IMPORT_TARGETS, maskSecretUrl, urlOfFormat, type ImportTarget } from './subscription'

const QR_SIZE = 220
const ICON_SIZE = 14

const loadSubscription = (signal: AbortSignal) => api.GET('/api/user/subscription', { signal })

/** 快速导入卡：各客户端一键导入、复制订阅地址、二维码、重置订阅（docs/03 §5.4）。 */
export function SubscriptionCard() {
  const { t } = useI18n()
  const toast = useToast()
  const copy = useCopy()
  const sub = useRemote<SubscriptionInfo>(loadSubscription)
  const [override, setOverride] = useState<SubscriptionInfo | null>(null)
  const [isRevealed, setIsRevealed] = useState(false)
  const [qrTarget, setQrTarget] = useState<ImportTarget | null>(null)
  const [isConfirming, setIsConfirming] = useState(false)
  const [isResetting, setIsResetting] = useState(false)
  const info = override ?? sub.data

  const handleImport = useCallback(
    (target: ImportTarget, current: SubscriptionInfo) => {
      const url = urlOfFormat(current, target.format)
      if (target.buildUrl === null) {
        void copy(url, t('sub.copiedFormat', { client: target.label }))
        return
      }
      window.location.assign(target.buildUrl(url, BRAND))
    },
    [copy, t],
  )

  const handleReset = async () => {
    setIsResetting(true)
    let next: SubscriptionInfo | null = null
    const err = await mutate(async () => {
      const res = await api.POST('/api/user/subscription/reset', { params: csrfHeader() })
      next = res.data?.data ?? null
      return res
    })
    setIsResetting(false)
    setIsConfirming(false)
    if (err !== null || next === null) {
      toast.show(err?.message ?? t('sub.resetFailed'), 'error')
      return
    }
    setOverride(next)
    setIsRevealed(false)
    toast.show(t('sub.resetDone'), 'success')
  }

  if (sub.error !== null && info === null) {
    return (
      <Card title={t('sub.title')}>
        <Alert tone="plain">{sub.error.message}</Alert>
      </Card>
    )
  }
  if (info === null) {
    return (
      <Card title={t('sub.title')}>
        <Loading isInline />
      </Card>
    )
  }

  return (
    <Card
      title={t('sub.title')}
      extra={
        <Button variant="outline" onClick={() => setIsConfirming(true)}>
          <RefreshCw size={ICON_SIZE} />
          {t('sub.reset')}
        </Button>
      }
    >
      <div className="hub-actions" style={{ marginBottom: 20 }}>
        {IMPORT_TARGETS.map((target) => (
          <Button
            key={target.key}
            className="hub-btn-brand hub-btn-round"
            style={{ '--brand-from': target.gradient[0], '--brand-to': target.gradient[1] } as CSSProperties}
            onClick={() => handleImport(target, info)}
          >
            {t(target.buildUrl === null ? 'sub.copyFor' : 'sub.importTo', { client: target.label })}
          </Button>
        ))}
      </div>
      <div className="hub-field-label">{t('sub.urlLabel')}</div>
      <div className="hub-copy-row">
        <input className="hub-input hub-mono" readOnly aria-label={t('sub.url')} value={isRevealed ? info.url : maskSecretUrl(info.url)} />
        <Button variant="outline" aria-label={t(isRevealed ? 'sub.hideUrl' : 'sub.showUrl')} onClick={() => setIsRevealed((v) => !v)}>
          {isRevealed ? <EyeOff size={ICON_SIZE} /> : <Eye size={ICON_SIZE} />}
        </Button>
        <Button variant="outline" aria-label={t('sub.copyUrl')} onClick={() => void copy(info.url, t('sub.urlCopied'))}>
          <Copy size={ICON_SIZE} />
        </Button>
        <Button variant="outline" aria-label={t('sub.showQr')} onClick={() => setQrTarget(IMPORT_TARGETS[0] ?? null)}>
          <QrCode size={ICON_SIZE} />
        </Button>
      </div>
      <p className="hub-field-hint">{t('sub.updateHint', { hours: info.updateIntervalHours })}</p>

      <Modal title={t('sub.qrTitle')} isOpen={qrTarget !== null} onClose={() => setQrTarget(null)}>
        <div className="hub-actions" style={{ marginBottom: 16 }}>
          {IMPORT_TARGETS.map((target) => (
            <Button key={target.key} variant={qrTarget?.key === target.key ? 'primary' : 'outline'} onClick={() => setQrTarget(target)}>
              {target.label}
            </Button>
          ))}
        </div>
        {qrTarget !== null && (
          <div style={{ display: 'grid', placeItems: 'center' }}>
            <QRCodeSVG value={urlOfFormat(info, qrTarget.format)} size={QR_SIZE} />
          </div>
        )}
      </Modal>
      <Confirm title={t('sub.resetConfirmTitle')} isOpen={isConfirming} isBusy={isResetting} confirmText={t('sub.resetConfirm')} onConfirm={() => void handleReset()} onClose={() => setIsConfirming(false)}>
        {t('sub.resetConfirmBody')}
      </Confirm>
    </Card>
  )
}
