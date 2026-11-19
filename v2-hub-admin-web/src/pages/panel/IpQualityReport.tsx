import { Alert, Collapse, Descriptions, Table, Tag, Typography, type TableProps } from 'antd'
import type { IpCheck, IpCheckResult, IpCheckStack, IpSourceResult } from '../../api/client'
import { formatCountry } from './countries'
import {
  EMPTY,
  FACTOR_KEYS,
  FACTOR_NAME,
  MAIL_NAME,
  MEDIA_NAME,
  SOURCE_NAME,
  boolText,
  labelOf,
  type Label,
} from './ip-quality-labels'

/** IP 质量检测的完整报告（docs/03 §5.23 展示）：六个分区，没配的源灰显“未配置”，服务器侧没做的写明原因。 */

const SECTION_LEVEL = 5
const SCORE_DECIMALS = 2

type MediaItem = IpCheckStack['media']['items'][number]
type MailItem = IpCheckStack['mail']['items'][number]
type DnsblItem = IpCheckStack['dnsbl']['items'][number]

/** 一个枚举值的标签；没有颜色时也用 Tag，保持对齐。 */
export function LabelTag({ label }: { label: Label }) {
  if (label.text === EMPTY) {
    return <>{EMPTY}</>
  }
  return <Tag color={label.color}>{label.text}</Tag>
}

function sourceName(key: string): string {
  return SOURCE_NAME[key] ?? key
}

/** 没配 key 的源整行灰显。 */
function rowClass(s: IpSourceResult): string {
  return s.status === 'ok' ? '' : 'ipq-muted'
}

const typeColumns: TableProps<IpSourceResult>['columns'] = [
  { title: '数据源', key: 'name', render: (_: unknown, s: IpSourceResult) => sourceName(s.key) },
  { title: '状态', dataIndex: 'status', render: (v: string) => <LabelTag label={labelOf.sourceStatus(v)} /> },
  { title: '使用类型', dataIndex: 'usageType', render: (v: string) => <LabelTag label={labelOf.netType(v)} /> },
  { title: '公司类型', dataIndex: 'companyType', render: (v: string) => <LabelTag label={labelOf.netType(v)} /> },
]

const scoreColumns: TableProps<IpSourceResult>['columns'] = [
  { title: '数据源', key: 'name', render: (_: unknown, s: IpSourceResult) => sourceName(s.key) },
  { title: '状态', dataIndex: 'status', render: (v: string) => <LabelTag label={labelOf.sourceStatus(v)} /> },
  { title: '风险分', key: 'value', align: 'right', render: (_: unknown, s: IpSourceResult) => (s.score ? s.score.value.toFixed(SCORE_DECIMALS) : EMPTY) },
  { title: '等级', key: 'level', render: (_: unknown, s: IpSourceResult) => (s.score ? <LabelTag label={labelOf.risk(s.score.level)} /> : EMPTY) },
]

const mediaColumns: TableProps<MediaItem>['columns'] = [
  { title: '服务', dataIndex: 'key', render: (k: string) => MEDIA_NAME[k] ?? k },
  { title: '状态', dataIndex: 'status', render: (v: string) => <LabelTag label={labelOf.media(v)} /> },
  { title: '地区', dataIndex: 'region', render: (v: string) => formatCountry(v) },
  { title: '解锁方式', dataIndex: 'method', render: (v: string) => <LabelTag label={labelOf.method(v)} /> },
  {
    title: '证据',
    dataIndex: 'evidence',
    render: (ev: Record<string, string>) => (
      <Typography.Text type="secondary" className="mono">
        {Object.entries(ev)
          .map(([k, v]) => `${k}=${v}`)
          .join(' ')}
      </Typography.Text>
    ),
  },
]

const mailColumns: TableProps<MailItem>['columns'] = [
  { title: '邮局', dataIndex: 'key', render: (k: string) => MAIL_NAME[k] ?? k },
  { title: '25 端口', dataIndex: 'result', render: (v: string) => <LabelTag label={labelOf.mail(v)} /> },
  { title: '应答码', dataIndex: 'code', render: (v: string) => v || EMPTY },
  { title: 'MX', dataIndex: 'mx', render: (v: string) => <span className="mono">{v || EMPTY}</span> },
]

const dnsblColumns: TableProps<DnsblItem>['columns'] = [
  { title: '列表', dataIndex: 'key', render: (v: string) => <span className="mono">{v}</span> },
  { title: '结论', dataIndex: 'result', render: (v: string) => <LabelTag label={labelOf.dnsbl(v)} /> },
  { title: '返回', dataIndex: 'answer', render: (v: string) => <span className="mono">{v || EMPTY}</span> },
]

function FactorMatrix({ sources }: { sources: IpSourceResult[] }) {
  interface FactorRow {
    key: string
    name: string
  }
  const rows: FactorRow[] = FACTOR_KEYS.map((k) => ({ key: k, name: FACTOR_NAME[k] }))
  const columns: TableProps<FactorRow>['columns'] = [
    { title: '因子', dataIndex: 'name', fixed: 'left' },
    ...sources.map((s) => ({
      title: sourceName(s.key),
      key: s.key,
      render: (_: unknown, row: FactorRow) => {
        if (row.key === 'country') {
          return s.factors.country || EMPTY
        }
        return <LabelTag label={boolText(s.factors[row.key as Exclude<(typeof FACTOR_KEYS)[number], 'country'>])} />
      },
    })),
  ]
  return <Table<FactorRow> rowKey="key" size="small" pagination={false} scroll={{ x: true }} columns={columns} dataSource={rows} />
}

function HostSideNote({ status }: { status: string }) {
  if (status === 'done') {
    return null
  }
  return <Alert type="info" showIcon className="page-alert" title={`服务器侧没做：${labelOf.hostProbe(status).text}`} />
}

interface IpQualityReportProps {
  check: IpCheck
  result: IpCheckResult | null
}

/** 一次检测的完整报告；还没执行完或整次失败时只显示状态与原因。 */
export function IpQualityReport({ check, result }: IpQualityReportProps) {
  const st = result?.stacks?.v4
  if (check.status === 'failed') {
    return <Alert type="error" showIcon title={`检测失败：${labelOf.error(check.errorCode).text}`} description={check.lastError} />
  }
  if (st === undefined) {
    return <Typography.Paragraph type="secondary">检测还没结束。</Typography.Paragraph>
  }
  const b = st.basics
  return (
    <>
      <Typography.Title level={SECTION_LEVEL}>基础信息</Typography.Title>
      <Descriptions size="small" bordered column={{ xs: 1, md: 2 }}>
        <Descriptions.Item label="被测 IP">
          <span className="mono">{st.ip}</span>（{labelOf.ipSource(st.ipSource).text}）
        </Descriptions.Item>
        <Descriptions.Item label="IP 类型">
          <LabelTag label={labelOf.ipKind(b.ipKind)} />
        </Descriptions.Item>
        <Descriptions.Item label="使用地">{[formatCountry(b.country), b.region, b.city].filter((v) => v !== '').join(' · ')}</Descriptions.Item>
        <Descriptions.Item label="注册地">
          {formatCountry(b.regCountry)}
          {b.registry !== '' && `（${b.registry}）`}
        </Descriptions.Item>
        <Descriptions.Item label="ASN">{b.asn === null ? EMPTY : `AS${b.asn} ${b.asOrg}`}</Descriptions.Item>
        <Descriptions.Item label="BGP 前缀">
          <span className="mono">{b.prefix || EMPTY}</span>
        </Descriptions.Item>
        <Descriptions.Item label="时区">{b.timezone || EMPTY}</Descriptions.Item>
        <Descriptions.Item label="服务器出口">
          {st.egress.ip === '' ? EMPTY : `${st.egress.ip}（${formatCountry(st.egress.country)}）`}
        </Descriptions.Item>
      </Descriptions>

      <Typography.Title level={SECTION_LEVEL}>IP 类型</Typography.Title>
      <Table<IpSourceResult> rowKey="key" size="small" pagination={false} rowClassName={rowClass} columns={typeColumns} dataSource={st.sources} />

      <Typography.Title level={SECTION_LEVEL}>风险评分</Typography.Title>
      <Table<IpSourceResult> rowKey="key" size="small" pagination={false} rowClassName={rowClass} columns={scoreColumns} dataSource={st.sources} />

      <Typography.Title level={SECTION_LEVEL}>风险因子</Typography.Title>
      <FactorMatrix sources={st.sources.filter((s) => s.status === 'ok')} />

      <Typography.Title level={SECTION_LEVEL}>流媒体与 AI</Typography.Title>
      <HostSideNote status={st.media.status} />
      {st.media.items.length > 0 && <Table<MediaItem> rowKey="key" size="small" pagination={false} scroll={{ x: true }} columns={mediaColumns} dataSource={st.media.items} />}

      <Typography.Title level={SECTION_LEVEL}>邮局与黑名单</Typography.Title>
      <HostSideNote status={st.mail.status} />
      {st.mail.items.length > 0 && (
        <>
          <Typography.Paragraph>
            本机 25 端口出站：<LabelTag label={labelOf.port25(st.mail.port25)} />
          </Typography.Paragraph>
          <Table<MailItem> rowKey="key" size="small" pagination={false} columns={mailColumns} dataSource={st.mail.items} />
        </>
      )}
      <Typography.Paragraph className="page-alert">
        DNS 黑名单：上榜 {st.dnsbl.listed}、策略类 {st.dnsbl.policy}、未上榜 {st.dnsbl.clean}、不可用 {st.dnsbl.unavailable}
        {st.dnsbl.unavailable > 0 && '（不可用多为查询被拒：平台机用的 DNS 解析器被列表拒绝，见配置 ipQuality.dnsResolver）'}
      </Typography.Paragraph>
      <Collapse
        size="small"
        items={[{ key: 'dnsbl', label: '各列表结论', children: <Table<DnsblItem> rowKey="key" size="small" pagination={false} columns={dnsblColumns} dataSource={st.dnsbl.items} /> }]}
      />
    </>
  )
}
