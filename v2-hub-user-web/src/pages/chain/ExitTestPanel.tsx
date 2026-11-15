import { useId, useState } from 'react'
import { api, csrfHeader, type ChainExit, type ChainExitTestMode, type ChainExitTestResult, type ChainNodeOption } from '../../api/client'
import { Alert, Badge, Button, Segmented, SelectField } from '../../components/ui'
import { useI18n, type MessageKey } from '../../i18n/i18n-context'
import { request } from '../../hooks/use-remote'

/** 三种测试方式，按从快到慢排（docs/03 §5.21 延迟测试）。 */
const TEST_MODES: readonly { mode: ChainExitTestMode; labelKey: MessageKey }[] = [
  { mode: 'tcp', labelKey: 'chain.testModeTcp' },
  { mode: 'http', labelKey: 'chain.testModeHttp' },
  { mode: 'real', labelKey: 'chain.testModeReal' },
]
const DEFAULT_MODE: ChainExitTestMode = 'tcp'
const NO_HTTP_STATUS = 0

/** 不通的原因大类对应的标题与排查提示（docs/03 §5.21，Ruling-56）；unknown 与 none 没有提示。 */
const FAIL_TEXT: Record<ChainExitTestResult['reason'], { titleKey: MessageKey; hintKey: MessageKey | null }> = {
  none: { titleKey: 'chain.testFail', hintKey: null },
  unknown: { titleKey: 'chain.testFail', hintKey: null },
  refused: { titleKey: 'chain.testFailRefused', hintKey: 'chain.testFailRefusedHint' },
  timeout: { titleKey: 'chain.testFailTimeout', hintKey: 'chain.testFailTimeoutHint' },
  proxy_rejected: { titleKey: 'chain.testFailProxyRejected', hintKey: 'chain.testFailProxyRejectedHint' },
  proxy_no_response: { titleKey: 'chain.testFailProxyNoResponse', hintKey: 'chain.testFailProxyNoResponseHint' },
  bad_status: { titleKey: 'chain.testFailBadStatus', hintKey: 'chain.testFailBadStatusHint' },
}

interface ExitTestPanelProps {
  exit: ChainExit
  /** 可作起点的节点：本人可用、所在面板支持测试的。 */
  nodes: ChainNodeOption[]
}

/**
 * 出口的延迟测试（docs/06 §5 M10，Ruling-52）：选起点节点与方式，由起点所在的服务器去连出口。
 * 结果只留在本组件的内存里；失败时后端只给原因大类（Ruling-56），这里换成一句说明和排查提示。
 */
export function ExitTestPanel({ exit, nodes }: ExitTestPanelProps) {
  const { t } = useI18n()
  const modeLabelId = useId()
  const [nodeId, setNodeId] = useState<number | null>(nodes[0]?.id ?? null)
  const [mode, setMode] = useState<ChainExitTestMode>(DEFAULT_MODE)
  const [isBusy, setIsBusy] = useState(false)
  const [result, setResult] = useState<ChainExitTestResult | null>(null)
  const [error, setError] = useState<string | null>(null)

  const run = async () => {
    if (nodeId === null) {
      return
    }
    setIsBusy(true)
    setError(null)
    setResult(null)
    try {
      const result = await request(() => api.POST('/api/user/chain/exits/{id}/test', { params: { ...csrfHeader(), path: { id: exit.id } }, body: { nodeId, mode } }))
      if (result.error !== null) {
        setError(result.error.message)
        return
      }
      setResult(result.data)
    } finally {
      setIsBusy(false)
    }
  }

  if (nodes.length === 0) {
    return <Alert tone="info">{t('chain.testNoNodes')}</Alert>
  }
  const failText = result !== null && !result.ok ? FAIL_TEXT[result.reason] : null
  return (
    <div className="hub-exit-test">
      <SelectField label={t('chain.testFrom')} value={nodeId ?? ''} onChange={(e) => setNodeId(Number(e.target.value))}>
        {nodes.map((n) => (
          <option key={n.id} value={n.id}>
            {n.name}
          </option>
        ))}
      </SelectField>
      <div className="hub-field">
        <span className="hub-field-label" id={modeLabelId}>
          {t('chain.testMode')}
        </span>
        <Segmented
          aria="pressed"
          className="hub-exit-seg"
          aria-labelledby={modeLabelId}
          options={TEST_MODES.map((m) => ({ value: m.mode, label: t(m.labelKey) }))}
          value={mode}
          onChange={setMode}
        />
      </div>
      <div className="hub-actions">
        <Button variant="primary" isBusy={isBusy} onClick={() => void run()}>
          {t('chain.testRun')}
        </Button>
      </div>
      {error !== null && <Alert tone="plain">{error}</Alert>}
      {result !== null && (
        <p className="hub-exit-test-result" role="status">
          {failText === null ? (
            <Badge tone="success">{t('chain.testOk', { ms: result.delayMs })}</Badge>
          ) : (
            <Badge tone="danger">{t(failText.titleKey)}</Badge>
          )}
          {result.httpStatus !== NO_HTTP_STATUS && <span>{t('chain.testHttpStatus', { code: result.httpStatus })}</span>}
          {result.egressIp !== '' && (
            <span className="hub-mono">
              {t('chain.testEgress', { ip: result.egressIp })}
              {result.egressRegion !== '' && ` (${result.egressRegion})`}
            </span>
          )}
        </p>
      )}
      {failText !== null && failText.hintKey !== null && <Alert tone="plain">{t(failText.hintKey)}</Alert>}
      <p className="hub-field-hint">{t('chain.testHint')}</p>
    </div>
  )
}
