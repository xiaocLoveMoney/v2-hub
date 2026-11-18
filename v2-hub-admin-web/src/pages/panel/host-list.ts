import { MAX_NAME_LEN } from '../form-limits'

/** 一次批量新建的主机数上限（docs/03 §5.22 批量）。 */
export const MAX_HOST_BATCH = 50
/** 主机地址的最大长度（hub-api.yaml HostAddress）。 */
export const MAX_HOST_ADDRESS_LEN = 255
/** 在部署平台的机器上托管它自己时地址怎么填（docs/03 §5.22 “与平台同机”，Ruling-75）：平台跑在容器里，回环是容器自己。 */
export const SAME_MACHINE_ADDRESS_HINT =
  '平台和面板在同一台机器时填这台机器的公网 IP（不选 DNS 账号时也可填 Docker 网关 172.17.0.1），不能填 127.0.0.1'
/** SSH 端口的默认值。 */
export const DEFAULT_SSH_PORT = 22

/** 清单里解析出的一台主机；line 是它在文本里的行号（从 1 起）。 */
export interface HostListEntry {
  line: number
  address: string
  name: string
}

/** 清单里一行的问题。 */
export interface HostListError {
  line: number
  message: string
}

/** 主机清单的解析结果：errors 为空才能提交。 */
export interface HostListResult {
  hosts: HostListEntry[]
  errors: HostListError[]
}

const LINE_BREAK_PATTERN = /\r?\n/
const BLANK_PATTERN = /\s+/
const DIGITS_AND_DOTS_PATTERN = /^[\d.]+$/
const IPV4_PATTERN = /^\d{1,3}(\.\d{1,3}){3}$/
const IPV6_CHARS_PATTERN = /^[0-9a-fA-F:.]+$/
// 域名的一段：字母数字开头结尾，中间可有连字符，最长 63
const DOMAIN_LABEL_PATTERN = /^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$/
const MAX_IPV4_OCTET = 255
const MAX_DOMAIN_LEN = 253
/** IPv6 至少有两个冒号；只有一个冒号的多半是“地址:端口”。 */
const MIN_IPV6_COLONS = 2
/** 一行最多两段：地址、名称。 */
const MAX_LINE_FIELDS = 2
/** 校验提示里最多列出几行的问题，其余只报个数。 */
const MAX_SHOWN_ERRORS = 5

function isIpv4(address: string): boolean {
  return IPV4_PATTERN.test(address) && address.split('.').every((octet) => Number(octet) <= MAX_IPV4_OCTET)
}

function isIpv6(address: string): boolean {
  return IPV6_CHARS_PATTERN.test(address) && address.split(':').length > MIN_IPV6_COLONS
}

function isDomain(address: string): boolean {
  return address.length <= MAX_DOMAIN_LEN && address.split('.').every((label) => DOMAIN_LABEL_PATTERN.test(label))
}

/**
 * 主机地址的写法检查：IPv4、IPv6 或域名，不带协议头与端口。
 * 只查写法；回环、未指定地址等策略由后端判断（docs/03 §5.22 主机）。
 * @param address 已去掉首尾空白的地址
 * @returns 有问题时的说明，没问题为 null
 */
export function hostAddressError(address: string): string | null {
  if (address === '') {
    return '地址不能为空'
  }
  if (address.length > MAX_HOST_ADDRESS_LEN) {
    return `地址不能超过 ${MAX_HOST_ADDRESS_LEN} 个字符`
  }
  if (DIGITS_AND_DOTS_PATTERN.test(address)) {
    return isIpv4(address) ? null : '不是合法的 IPv4 地址'
  }
  if (address.includes(':')) {
    return isIpv6(address) ? null : '地址写法不对：只填 IP 或域名，不要带协议头或端口'
  }
  return isDomain(address) ? null : '地址写法不对：只填 IP 或域名'
}

/**
 * 地址字段的表单校验（antd Form 的 validator）：去掉首尾空白后按 hostAddressError 检查。
 * @param _ 规则对象，不用
 * @param value 输入框的值
 * @throws Error 写法不对时，message 是给管理员看的说明
 */
export async function validateHostAddressField(_: unknown, value: string | undefined): Promise<void> {
  const problem = hostAddressError((value ?? '').trim())
  if (problem !== null) {
    throw new Error(problem)
  }
}

/** 解析一行（已去掉首尾空白、非空）：成功返回地址与名称，失败返回说明。 */
function parseLine(text: string): { address: string; name: string } | string {
  const fields = text.split(BLANK_PATTERN)
  if (fields.length > MAX_LINE_FIELDS) {
    return '每行只写“地址 名称”两段，名称里不能有空白'
  }
  const address = fields[0] ?? ''
  const addressError = hostAddressError(address)
  if (addressError !== null) {
    return addressError
  }
  const givenName = fields[1]
  if (givenName === undefined && address.length > MAX_NAME_LEN) {
    return `没写名称时用地址当名称，但地址超过 ${MAX_NAME_LEN} 个字符，请写上名称`
  }
  const name = givenName ?? address
  if (name.length > MAX_NAME_LEN) {
    return `名称不能超过 ${MAX_NAME_LEN} 个字符`
  }
  return { address, name }
}

/**
 * 解析批量新建的主机清单（docs/03 §5.22 批量）：每行“地址 名称”，空白分隔，名称省略时用地址当名称；
 * 空行跳过但仍计行号。逐行校验写法、长度，以及清单内地址、名称是否重复；超过 MAX_HOST_BATCH 台时在超出的第一行报错。
 * @param text 文本框里的原文
 * @returns 合法的主机（按出现顺序）与各行的问题；清单为空时两者都是空数组
 */
export function parseHostList(text: string): HostListResult {
  const hosts: HostListEntry[] = []
  const errors: HostListError[] = []
  const addressLines = new Map<string, number>()
  const nameLines = new Map<string, number>()
  let filledCount = 0

  text.split(LINE_BREAK_PATTERN).forEach((raw, index) => {
    const line = index + 1
    const trimmed = raw.trim()
    if (trimmed === '') {
      return
    }
    filledCount += 1
    if (filledCount > MAX_HOST_BATCH) {
      if (filledCount === MAX_HOST_BATCH + 1) {
        errors.push({ line, message: `一次最多 ${MAX_HOST_BATCH} 台，从这一行起超出` })
      }
      return
    }
    const parsed = parseLine(trimmed)
    if (typeof parsed === 'string') {
      errors.push({ line, message: parsed })
      return
    }
    const addressKey = parsed.address.toLowerCase()
    const sameAddressLine = addressLines.get(addressKey)
    if (sameAddressLine !== undefined) {
      errors.push({ line, message: `地址与第 ${sameAddressLine} 行重复` })
      return
    }
    const sameNameLine = nameLines.get(parsed.name)
    if (sameNameLine !== undefined) {
      errors.push({ line, message: `名称与第 ${sameNameLine} 行重复` })
      return
    }
    addressLines.set(addressKey, line)
    nameLines.set(parsed.name, line)
    hosts.push({ line, address: parsed.address, name: parsed.name })
  })
  return { hosts, errors }
}

/**
 * 从面板地址里取主机名，给“绑定 SSH”预填地址用（docs/03 §5.22 给已接入的服务器绑定 SSH）。
 * @param baseUrl 面板地址，如 https://panel.example.com:2053/abc
 * @returns 主机名（IPv6 不带方括号）；不是合法地址时为空串
 */
export function hostnameOfUrl(baseUrl: string): string {
  try {
    const { hostname } = new URL(baseUrl)
    return hostname.startsWith('[') ? hostname.slice(1, -1) : hostname
  } catch {
    // 不是合法的地址：不预填，由管理员自己填
    return ''
  }
}

/** “地址:端口”的显示写法；IPv6 地址加方括号。 */
export function formatHostEndpoint(address: string, port: number): string {
  return address.includes(':') ? `[${address}]:${port}` : `${address}:${port}`
}

/**
 * 把各行的问题拼成一条表单提示：“第 3 行：…；第 7 行：…”，超过 MAX_SHOWN_ERRORS 条时只列前几条并注明总数。
 * @param errors parseHostList 给出的问题，按行号升序
 * @returns 提示文字；没有问题时为空串
 */
export function formatHostListErrors(errors: readonly HostListError[]): string {
  const shown = errors.slice(0, MAX_SHOWN_ERRORS).map((e) => `第 ${e.line} 行：${e.message}`)
  if (errors.length > MAX_SHOWN_ERRORS) {
    shown.push(`共 ${errors.length} 处问题`)
  }
  return shown.join('；')
}
