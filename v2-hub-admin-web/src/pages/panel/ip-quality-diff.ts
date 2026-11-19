import type { IpCheckResult, IpCheckStack } from '../../api/client'
import { countryName } from './countries'
import {
  DNSBL_RESULT,
  EMPTY,
  FACTOR_KEYS,
  FACTOR_NAME,
  MAIL_NAME,
  MEDIA_NAME,
  SOURCE_NAME,
  boolText,
  labelOf,
} from './ip-quality-labels'

/**
 * 检测报告压平与逐项对比（docs/03 §5.23 展示，Ruling-81）：报告里都是枚举码、数组按 key 排序，
 * 所以压平成“路径 → 显示文字”后按路径比较即可；证据（evidence）不参与对比。
 */

/** 报告分区，顺序即显示顺序。 */
export const SECTIONS = ['基础信息', '风险评分与因子', '流媒体与 AI', '邮局与黑名单'] as const
export type Section = (typeof SECTIONS)[number]

/** 压平后的一项。 */
export interface FlatItem {
  /** 稳定的路径，如 v4.sources.ipqs.factors.vpn。 */
  path: string
  section: Section
  label: string
  text: string
}

/** 对比后的一行；某一边没有这一项时那一边为 EMPTY。 */
export interface DiffRow {
  path: string
  section: Section
  label: string
  older: string
  newer: string
  isChanged: boolean
}

const STACK_V4 = 'v4'
const SCORE_DECIMALS = 2

function countryText(code: string): string {
  return code === '' ? EMPTY : `${countryName(code)}（${code}）`
}

function scoreText(score: { value: number; level: string } | null): string {
  if (score === null) {
    return EMPTY
  }
  return `${score.value.toFixed(SCORE_DECIMALS)}（${labelOf.risk(score.level).text}）`
}

function flattenStack(prefix: string, st: IpCheckStack): FlatItem[] {
  const out: FlatItem[] = []
  const add = (path: string, section: Section, label: string, text: string) => out.push({ path: `${prefix}.${path}`, section, label, text: text === '' ? EMPTY : text })
  const b = st.basics
  add('ip', '基础信息', '被测 IP', st.ip)
  add('ipSource', '基础信息', 'IP 来源', labelOf.ipSource(st.ipSource).text)
  add('basics.country', '基础信息', '使用地', countryText(b.country))
  add('basics.region', '基础信息', '地区', b.region)
  add('basics.city', '基础信息', '城市', b.city)
  add('basics.timezone', '基础信息', '时区', b.timezone)
  add('basics.asn', '基础信息', 'ASN', b.asn === null ? '' : `AS${b.asn}`)
  add('basics.asOrg', '基础信息', '组织', b.asOrg)
  add('basics.prefix', '基础信息', 'BGP 前缀', b.prefix)
  add('basics.regCountry', '基础信息', '注册地', countryText(b.regCountry))
  add('basics.registry', '基础信息', '注册机构', b.registry)
  add('basics.ipKind', '基础信息', 'IP 类型', labelOf.ipKind(b.ipKind).text)
  add('egress.ip', '基础信息', '服务器出口 IP', st.egress.ip)
  add('egress.country', '基础信息', '服务器出口国家', countryText(st.egress.country))

  for (const s of st.sources) {
    const name = SOURCE_NAME[s.key] ?? s.key
    const p = `sources.${s.key}`
    add(`${p}.status`, '风险评分与因子', `${name} · 状态`, labelOf.sourceStatus(s.status).text)
    add(`${p}.usageType`, '风险评分与因子', `${name} · 使用类型`, labelOf.netType(s.usageType).text)
    add(`${p}.companyType`, '风险评分与因子', `${name} · 公司类型`, labelOf.netType(s.companyType).text)
    add(`${p}.score`, '风险评分与因子', `${name} · 风险分`, scoreText(s.score ?? null))
    for (const f of FACTOR_KEYS) {
      const text = f === 'country' ? countryText(s.factors.country) : boolText(s.factors[f]).text
      add(`${p}.factors.${f}`, '风险评分与因子', `${name} · ${FACTOR_NAME[f]}`, text)
    }
  }

  add('media.status', '流媒体与 AI', '服务器侧', labelOf.hostProbe(st.media.status).text)
  for (const m of st.media.items) {
    const name = MEDIA_NAME[m.key] ?? m.key
    add(`media.${m.key}.status`, '流媒体与 AI', `${name} · 状态`, labelOf.media(m.status).text)
    add(`media.${m.key}.region`, '流媒体与 AI', `${name} · 地区`, countryText(m.region))
    add(`media.${m.key}.method`, '流媒体与 AI', `${name} · 解锁方式`, labelOf.method(m.method).text)
  }

  add('mail.port25', '邮局与黑名单', '本机 25 端口出站', labelOf.port25(st.mail.port25).text)
  for (const m of st.mail.items) {
    add(`mail.${m.key}`, '邮局与黑名单', `邮局 · ${MAIL_NAME[m.key] ?? m.key}`, labelOf.mail(m.result).text)
  }
  add('dnsbl.listed', '邮局与黑名单', '黑名单 · 上榜', String(st.dnsbl.listed))
  add('dnsbl.policy', '邮局与黑名单', '黑名单 · 策略类', String(st.dnsbl.policy))
  add('dnsbl.unavailable', '邮局与黑名单', '黑名单 · 不可用', String(st.dnsbl.unavailable))
  for (const it of st.dnsbl.items) {
    add(`dnsbl.${it.key}`, '邮局与黑名单', `黑名单 · ${it.key}`, (DNSBL_RESULT[it.result] ?? { text: it.result }).text)
  }
  return out
}

/** 把报告压平成按显示顺序排列的项；报告为空返回空数组。 */
export function flattenResult(result: IpCheckResult | null | undefined): FlatItem[] {
  const st = result?.stacks?.[STACK_V4]
  if (st === undefined) {
    return []
  }
  return flattenStack(STACK_V4, st)
}

/**
 * 逐项对比两份报告：行的顺序先按较新报告、再补上只在较早报告里有的项；一边没有的项显示 EMPTY 并算作变化。
 * @param older 较早的报告
 * @param newer 较新的报告
 */
export function diffResults(older: IpCheckResult | null | undefined, newer: IpCheckResult | null | undefined): DiffRow[] {
  const a = flattenResult(older)
  const b = flattenResult(newer)
  const olderByPath = new Map(a.map((it) => [it.path, it]))
  const seen = new Set<string>()
  const rows: DiffRow[] = []
  for (const it of b) {
    seen.add(it.path)
    const prev = olderByPath.get(it.path)
    const olderText = prev?.text ?? EMPTY
    rows.push({ path: it.path, section: it.section, label: it.label, older: olderText, newer: it.text, isChanged: prev === undefined || prev.text !== it.text })
  }
  for (const it of a) {
    if (!seen.has(it.path)) {
      rows.push({ path: it.path, section: it.section, label: it.label, older: it.text, newer: EMPTY, isChanged: true })
    }
  }
  const order = new Map(SECTIONS.map((s, i) => [s, i]))
  return rows
    .map((r, i) => ({ r, i }))
    .sort((x, y) => (order.get(x.r.section) ?? 0) - (order.get(y.r.section) ?? 0) || x.i - y.i)
    .map((x) => x.r)
}
