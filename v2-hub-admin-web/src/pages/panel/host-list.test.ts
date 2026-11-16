import { describe, expect, it } from 'vitest'
import { MAX_HOST_BATCH, formatHostEndpoint, formatHostListErrors, hostAddressError, hostnameOfUrl, parseHostList } from './host-list'

describe('parseHostList', () => {
  it('每行“地址 名称”，空白分隔；名称省略时用地址；空行跳过但仍计行号', () => {
    const result = parseHostList('203.0.113.10 hk-01\n\n  node3.example.com  \r\n2001:db8::1\tv6-01\n')
    expect(result.errors).toEqual([])
    expect(result.hosts).toEqual([
      { line: 1, address: '203.0.113.10', name: 'hk-01' },
      { line: 3, address: 'node3.example.com', name: 'node3.example.com' },
      { line: 4, address: '2001:db8::1', name: 'v6-01' },
    ])
  })

  it('空清单没有主机也没有问题，由表单提示“至少一台”', () => {
    expect(parseHostList(' \n\n')).toEqual({ hosts: [], errors: [] })
  })

  it('逐行校验并指出第几行：非法 IP、带端口、带协议头、多于两段', () => {
    const result = parseHostList(['203.0.113.10 ok-01', '999.1.1.1 bad-ip', '203.0.113.11:22 with-port', 'https://a.example.com url', '203.0.113.12 two words'].join('\n'))
    expect(result.hosts.map((h) => h.name)).toEqual(['ok-01'])
    expect(result.errors.map((e) => e.line)).toEqual([2, 3, 4, 5])
    expect(result.errors[0]?.message).toContain('IPv4')
    expect(result.errors[1]?.message).toContain('端口')
    expect(result.errors[3]?.message).toContain('两段')
  })

  it('清单内地址（不分大小写）或名称重复时指出与第几行重复', () => {
    const result = parseHostList('a.example.com n1\nA.Example.com n2\n203.0.113.9 n1')
    expect(result.hosts).toHaveLength(1)
    expect(result.errors).toEqual([
      { line: 2, message: '地址与第 1 行重复' },
      { line: 3, message: '名称与第 1 行重复' },
    ])
  })

  it('名称超过 64 个字符报错；没写名称而地址超长时要求写名称', () => {
    const longName = 'n'.repeat(65)
    const longDomain = `${'a'.repeat(40)}.${'b'.repeat(40)}.example.com`
    const result = parseHostList(`203.0.113.10 ${longName}\n${longDomain}\n${longDomain} short`)
    expect(result.errors.map((e) => e.line)).toEqual([1, 2])
    expect(result.errors[1]?.message).toContain('请写上名称')
    expect(result.hosts).toEqual([{ line: 3, address: longDomain, name: 'short' }])
  })

  it(`超过 ${MAX_HOST_BATCH} 台时只在超出的第一行报一次`, () => {
    const lines = Array.from({ length: MAX_HOST_BATCH + 3 }, (_, i) => `10.0.${Math.floor(i / 200)}.${(i % 200) + 1} h-${i}`)
    const result = parseHostList(lines.join('\n'))
    expect(result.hosts).toHaveLength(MAX_HOST_BATCH)
    expect(result.errors).toHaveLength(1)
    expect(result.errors[0]?.line).toBe(MAX_HOST_BATCH + 1)
  })
})

describe('hostAddressError', () => {
  it('接受 IPv4、IPv6、域名与单段主机名', () => {
    for (const address of ['203.0.113.10', '2001:db8::1', '::1', 'node.example.com', 'bastion']) {
      expect(hostAddressError(address)).toBeNull()
    }
  })

  it('拒绝空串、超长、非法字符与不完整的 IP', () => {
    for (const address of ['', '1.2.3', '256.1.1.1', 'a_b.example.com', '-bad.example.com', 'a..b', 'x'.repeat(256)]) {
      expect(hostAddressError(address)).not.toBeNull()
    }
  })
})

describe('清单相关的小工具', () => {
  it('formatHostListErrors 带行号，超过 5 条只列前 5 条并注明总数', () => {
    expect(formatHostListErrors([])).toBe('')
    expect(formatHostListErrors([{ line: 3, message: '不对' }])).toBe('第 3 行：不对')
    const many = Array.from({ length: 7 }, (_, i) => ({ line: i + 1, message: 'x' }))
    const text = formatHostListErrors(many)
    expect(text).toContain('第 5 行')
    expect(text).not.toContain('第 6 行')
    expect(text).toContain('共 7 处问题')
  })

  it('hostnameOfUrl 取面板地址里的主机名，IPv6 去掉方括号，非法地址为空串', () => {
    expect(hostnameOfUrl('https://panel.example.com:2053/abc')).toBe('panel.example.com')
    expect(hostnameOfUrl('http://[2001:db8::1]:2053/')).toBe('2001:db8::1')
    expect(hostnameOfUrl('not a url')).toBe('')
  })

  it('formatHostEndpoint 给 IPv6 加方括号', () => {
    expect(formatHostEndpoint('203.0.113.10', 22)).toBe('203.0.113.10:22')
    expect(formatHostEndpoint('2001:db8::1', 2222)).toBe('[2001:db8::1]:2222')
  })
})
