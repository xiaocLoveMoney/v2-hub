import { Alert, Button, Form, Input, Modal, Space, Table, Tag, Typography, type TableProps } from 'antd'
import { useState } from 'react'
import type { RealityScanResult } from '../../../api/client'
import type { Binder } from './fields'
import { MAX_REALITY_TARGETS, REALITY_KEY, fieldId, getAt, parseRealityTargets, realityServerNames, type Path } from './model'

const REALITY_BASE: Path = ['streamSettings', REALITY_KEY]
const TARGET_PATH: Path = [...REALITY_BASE, 'target']
const SERVER_NAMES_PATH: Path = [...REALITY_BASE, 'serverNames']
const XVER_PATH: Path = [...REALITY_BASE, 'xver']
const CUSTOM_ROWS = 3
const FINDER_WIDTH = 860
const DASH = '—'
const MSG_TOO_MANY = `一次最多探测 ${MAX_REALITY_TARGETS} 个目标`

/** 扫描 REALITY 目标的两个动作；失败（已提示）返回 null。 */
export interface RealityScanApi {
  scanOne: (target: string, sni: string, xver: number) => Promise<RealityScanResult | null>
  /** targets 为空 = 探测面板自己的候选站点。 */
  scanMany: (targets: string[]) => Promise<RealityScanResult[] | null>
}

function str(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

function strList(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []
}

/** 结论标签：可用 / 内网地址（没有探测）/ 不可用。 */
function VerdictTag({ result }: { result: RealityScanResult }) {
  if (result.feasible) {
    return <Tag color="green">可用</Tag>
  }
  return result.privateTarget ? <Tag color="orange">内网地址</Tag> : <Tag color="red">不可用</Tag>
}

/** “扫描”之后显示在目标输入框下方的一行结论。 */
function ScanSummary({ result }: { result: RealityScanResult }) {
  const details = [
    result.feasible ? `${result.latencyMs} ms` : '',
    result.tlsVersion === '' ? '' : `TLS ${result.tlsVersion}`,
    result.alpn,
    result.certSubject === '' ? '' : `证书 ${result.certSubject}`,
    result.feasible ? '' : result.reason,
    result.privateTarget ? '目标解析到内网地址，没有探测' : '',
  ].filter((d) => d !== '')
  return (
    <div className="form-hint" role="status">
      <VerdictTag result={result} />
      <Typography.Text type="secondary">{details.join(' · ')}</Typography.Text>
    </div>
  )
}

interface FinderProps {
  isOpen: boolean
  scan: RealityScanApi
  onPick: (result: RealityScanResult) => void
  onClose: () => void
}

/** “查找目标”弹窗：探测面板的候选站点或自己填的一组，按可用优先、延迟从低到高列出，选用后回填表单。 */
function RealityTargetFinder({ isOpen, scan, onPick, onClose }: FinderProps) {
  const [custom, setCustom] = useState('')
  const [isBusy, setIsBusy] = useState(false)
  const [results, setResults] = useState<RealityScanResult[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  const run = async () => {
    const targets = parseRealityTargets(custom)
    if (targets.length > MAX_REALITY_TARGETS) {
      setError(MSG_TOO_MANY)
      return
    }
    setError(null)
    setIsBusy(true)
    const res = await scan.scanMany(targets)
    setIsBusy(false)
    if (res !== null) {
      setResults(res)
    }
  }

  const columns: TableProps<RealityScanResult>['columns'] = [
    { title: '目标', dataIndex: 'target', render: (v: string) => <span className="mono">{v}</span> },
    { title: '结论', key: 'verdict', render: (_: unknown, r: RealityScanResult) => <VerdictTag result={r} /> },
    { title: '延迟', key: 'latency', render: (_: unknown, r: RealityScanResult) => (r.feasible ? `${r.latencyMs} ms` : DASH) },
    { title: 'TLS', key: 'tls', render: (_: unknown, r: RealityScanResult) => [r.tlsVersion, r.alpn].filter((x) => x !== '').join(' / ') || DASH },
    { title: '证书', dataIndex: 'certSubject', render: (v: string) => v || DASH },
    { title: '原因', dataIndex: 'reason', render: (v: string) => v || DASH },
    {
      title: '操作',
      key: 'actions',
      render: (_: unknown, r: RealityScanResult) => (
        <Button size="small" type={r.feasible ? 'primary' : 'default'} disabled={!r.feasible} aria-label={`选用 ${r.target}`} onClick={() => onPick(r)}>
          选用
        </Button>
      ),
    },
  ]

  return (
    <Modal title="查找 REALITY 目标" open={isOpen} onCancel={onClose} footer={null} destroyOnHidden width={FINDER_WIDTH}>
      <Typography.Paragraph type="secondary">
        由这台服务器的面板逐个探测（TLS 1.3、h2、X25519、可信证书），结果按可用优先、延迟从低到高排。候选留空 = 用面板自己的候选站点。
      </Typography.Paragraph>
      {error !== null && <Alert type="error" showIcon className="page-alert" title={error} />}
      <Form layout="vertical">
        <Form.Item label="自定义候选" htmlFor="reality-finder-targets" extra={`每行一个“域名或 IP[:端口]”，最多 ${MAX_REALITY_TARGETS} 个，不接受网段`}>
          <Input.TextArea id="reality-finder-targets" rows={CUSTOM_ROWS} className="mono" autoComplete="off" spellCheck={false} value={custom} onChange={(e) => setCustom(e.target.value)} />
        </Form.Item>
        <Button type="primary" loading={isBusy} onClick={() => void run()}>
          开始扫描
        </Button>
      </Form>
      {results !== null && <Table<RealityScanResult> className="form-hint" size="small" rowKey="target" columns={columns} dataSource={results} pagination={false} />}
    </Modal>
  )
}

/**
 * REALITY 的“目标”一行（docs/05 M10 页面，Ruling-54）：输入框旁“扫描”测当前填的目标，“查找目标”打开候选表。
 * 扫描结果只留在本组件的内存里；可用且 SNI 还空着时，用证书里的域名把 SNI 填上。
 */
export function RealityTargetField({ b, scan }: { b: Binder; scan: RealityScanApi }) {
  const [isScanning, setIsScanning] = useState(false)
  const [result, setResult] = useState<RealityScanResult | null>(null)
  const [isFinding, setIsFinding] = useState(false)
  const target = str(getAt(b.draft, TARGET_PATH))

  const scanCurrent = async () => {
    const names = strList(getAt(b.draft, SERVER_NAMES_PATH))
    const xver = getAt(b.draft, XVER_PATH)
    setIsScanning(true)
    setResult(null)
    const res = await scan.scanOne(target.trim(), names[0] ?? '', typeof xver === 'number' ? xver : 0)
    setIsScanning(false)
    if (res === null) {
      return
    }
    setResult(res)
    if (res.feasible && names.length === 0) {
      b.set(SERVER_NAMES_PATH, realityServerNames(res))
    }
  }
  const pick = (res: RealityScanResult) => {
    b.set(TARGET_PATH, res.target)
    b.set(SERVER_NAMES_PATH, realityServerNames(res))
    setResult(res)
    setIsFinding(false)
  }

  return (
    <>
      <Form.Item label="目标" extra="被伪装的网站，域名:端口" htmlFor={fieldId(TARGET_PATH)}>
        <Space.Compact className="full-width">
          <Input
            id={fieldId(TARGET_PATH)}
            value={target}
            placeholder="www.cloudflare.com:443"
            autoComplete="off"
            onChange={(e) => {
              b.set(TARGET_PATH, e.target.value)
              setResult(null)
            }}
          />
          <Button loading={isScanning} disabled={target.trim() === ''} onClick={() => void scanCurrent()}>
            扫描
          </Button>
          <Button onClick={() => setIsFinding(true)}>查找目标</Button>
        </Space.Compact>
        {result !== null && <ScanSummary result={result} />}
      </Form.Item>
      <RealityTargetFinder isOpen={isFinding} scan={scan} onPick={pick} onClose={() => setIsFinding(false)} />
    </>
  )
}
