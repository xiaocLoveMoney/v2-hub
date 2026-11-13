import { Copy, Eye, EyeOff, QrCode, RefreshCw } from 'lucide-react'
import { QRCodeSVG } from 'qrcode.react'
import { useCallback, useState, type CSSProperties } from 'react'
import { api, csrfHeader, type SubscriptionInfo } from '../api/client'
import { BRAND } from '../app-config'
import { useToast } from '../components/toast-context'
import { Alert, Button, Card, Confirm, Modal } from '../components/ui'
import { useCopy } from '../hooks/use-copy'
import { mutate, useRemote } from '../hooks/use-remote'
import { IMPORT_TARGETS, maskSecretUrl, urlOfFormat, type ImportTarget } from './subscription'

const QR_SIZE = 220
const ICON_SIZE = 14

const loadSubscription = (signal: AbortSignal) => api.GET('/api/user/subscription', { signal })

/** 快速导入卡：各客户端一键导入、复制订阅地址、二维码、重置订阅（docs/03 §5.4）。 */
export function SubscriptionCard() {
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
        void copy(url, `已复制 ${target.label} 订阅地址`)
        return
      }
      window.location.assign(target.buildUrl(url, BRAND))
    },
    [copy],
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
      toast.show(err?.message ?? '重置失败', 'error')
      return
    }
    setOverride(next)
    setIsRevealed(false)
    toast.show('已重置，请在客户端重新导入订阅', 'success')
  }

  if (sub.error !== null && info === null) {
    return (
      <Card title="快速导入">
        <Alert tone="plain">{sub.error.message}</Alert>
      </Card>
    )
  }
  if (info === null) {
    return (
      <Card title="快速导入">
        <div className="hub-empty">加载中…</div>
      </Card>
    )
  }

  return (
    <Card
      title="快速导入"
      extra={
        <Button variant="outline" onClick={() => setIsConfirming(true)}>
          <RefreshCw size={ICON_SIZE} />
          重置订阅
        </Button>
      }
    >
      <div className="hub-actions" style={{ marginBottom: 20 }}>
        {IMPORT_TARGETS.map((t) => (
          <Button
            key={t.key}
            className="hub-btn-brand hub-btn-round"
            style={{ '--brand-from': t.gradient[0], '--brand-to': t.gradient[1] } as CSSProperties}
            onClick={() => handleImport(t, info)}
          >
            {t.buildUrl === null ? `复制 ${t.label}` : `导入 ${t.label}`}
          </Button>
        ))}
      </div>
      <div className="hub-field-label">订阅地址（请勿泄露）</div>
      <div className="hub-copy-row">
        <input className="hub-input hub-mono" readOnly aria-label="订阅地址" value={isRevealed ? info.url : maskSecretUrl(info.url)} />
        <Button variant="outline" aria-label={isRevealed ? '隐藏订阅地址' : '显示订阅地址'} onClick={() => setIsRevealed((v) => !v)}>
          {isRevealed ? <EyeOff size={ICON_SIZE} /> : <Eye size={ICON_SIZE} />}
        </Button>
        <Button variant="outline" aria-label="复制订阅地址" onClick={() => void copy(info.url, '已复制订阅地址')}>
          <Copy size={ICON_SIZE} />
        </Button>
        <Button variant="outline" aria-label="显示订阅二维码" onClick={() => setQrTarget(IMPORT_TARGETS[0] ?? null)}>
          <QrCode size={ICON_SIZE} />
        </Button>
      </div>
      <p className="hub-field-hint">客户端每 {info.updateIntervalHours} 小时自动更新一次；地址泄露后请立即重置。</p>

      <Modal title="订阅二维码" isOpen={qrTarget !== null} onClose={() => setQrTarget(null)}>
        <div className="hub-actions" style={{ marginBottom: 16 }}>
          {IMPORT_TARGETS.map((t) => (
            <Button key={t.key} variant={qrTarget?.key === t.key ? 'primary' : 'outline'} onClick={() => setQrTarget(t)}>
              {t.label}
            </Button>
          ))}
        </div>
        {qrTarget !== null && (
          <div style={{ display: 'grid', placeItems: 'center' }}>
            <QRCodeSVG value={urlOfFormat(info, qrTarget.format)} size={QR_SIZE} />
          </div>
        )}
      </Modal>
      <Confirm title="重置订阅？" isOpen={isConfirming} isBusy={isResetting} confirmText="重置" onConfirm={() => void handleReset()} onClose={() => setIsConfirming(false)}>
        旧订阅地址与旧连接凭据会立即失效，所有设备都需要重新导入订阅。
      </Confirm>
    </Card>
  )
}
