import type { DnsAccount, HostTask, Server, ServerHost, SshCredential } from '../api/client'

// SSH 托管各测试文件共用的样例数据；地址用文档保留网段，指纹是示例值

const NOW = '2026-11-16T08:00:00Z'

/** 一条任务，默认是已成功的部署。 */
export function taskOf(over: Partial<HostTask> = {}): HostTask {
  return {
    id: 9,
    hostId: 5,
    kind: 'provision',
    status: 'succeeded',
    step: 'register',
    errorCode: '',
    lastError: '',
    createdByName: 'root',
    createdAt: NOW,
    startedAt: NOW,
    finishedAt: NOW,
    ...over,
  }
}

/** 一台主机，默认已关联服务器 1、最近任务成功。 */
export function hostOf(over: Partial<ServerHost> = {}): ServerHost {
  return {
    id: 5,
    name: 'hk-01',
    address: '203.0.113.10',
    sshPort: 22,
    credentialId: 3,
    credentialName: 'root-key',
    providerType: 'xui',
    region: 'HK',
    panelPort: null,
    hostKeySha256: 'SHA256:hostkey-example',
    osId: 'debian',
    arch: 'amd64',
    serverId: 1,
    serverName: 'hk-01',
    dnsAccountId: null,
    dnsAccountName: '',
    domain: '',
    certNotAfter: null,
    lastTask: taskOf(),
    createdAt: NOW,
    ...over,
  }
}

/** 一份私钥凭据。 */
export const CREDENTIAL: SshCredential = {
  id: 3,
  name: 'root-key',
  username: 'root',
  authType: 'key',
  keyFingerprint: 'SHA256:fingerprint-example',
  remark: '',
  hostCount: 2,
  createdAt: NOW,
  updatedAt: NOW,
}

/** 一个 Cloudflare 的 DNS 账号。 */
export const DNS_ACCOUNT: DnsAccount = {
  id: 7,
  name: 'cf-main',
  providerType: 'cloudflare',
  zone: 'example.com',
  remark: '',
  hostCount: 1,
  createdAt: NOW,
  updatedAt: NOW,
}

/** DNS 账号下拉框里这个账号的选项文字。 */
export const DNS_ACCOUNT_OPTION = 'cf-main（Cloudflare，example.com）'

/** 凭据下拉框里这份凭据的选项文字。 */
export const CREDENTIAL_OPTION = 'root-key（root，私钥）'

/** 服务器的状态卡片字段（Ruling-81、Ruling-82）：还没探测到资源、没填账单、本周期没有流量。 */
export const SERVER_STATUS_FIELDS: Pick<Server, 'resources' | 'publicIpv4' | 'publicIpv6' | 'onlineClients' | 'billing' | 'traffic' | 'host'> = {
  resources: null,
  publicIpv4: '',
  publicIpv6: '',
  onlineClients: 0,
  billing: { priceCents: null, priceCurrency: 'USD', billingCycle: 'month', expiresOn: null, trafficQuotaBytes: null, trafficResetDay: 1, trafficMode: 'both' },
  traffic: { cycleStart: '2026-11-01', nextResetOn: '2026-12-01', sentBytes: 0, recvBytes: 0, usedBytes: 0 },
  host: null,
}

/** 一台服务器，默认 HTTPS 面板、没绑定主机。 */
export function serverOf(over: Partial<Server> = {}): Server {
  return {
    id: 1,
    name: 'hk-1',
    providerType: 'xui',
    baseUrl: 'https://panel.example.com:2053/p',
    tlsMode: 'verify',
    certPinSha256: null,
    region: 'HK',
    remark: '',
    isDisabled: false,
    status: 'online',
    lastError: '',
    lastSeenAt: null,
    lastSyncedAt: null,
    panelVersion: '3.11.0',
    ...SERVER_STATUS_FIELDS,
    inboundCount: 0,
    createdAt: NOW,
    reach: null,
    hostId: null,
    clientFlow: '',
    ...over,
  }
}

/** 等这么久还没有新请求，就认为轮询已经停了。 */
export const QUIET_MS = 150

/** 真实等待一段时间。 */
export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, ms)
  })
}
