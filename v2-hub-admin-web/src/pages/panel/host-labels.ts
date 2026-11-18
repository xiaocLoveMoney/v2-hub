import type { DnsProviderType, HostDetection, HostErrorCode, HostTask, HostTaskKind, HostTaskStatus, SshAuthType } from '../../api/client'
import type { TagMeta } from './chain-labels'

/** 没有值时的占位。 */
export const EMPTY = '—'

/** 任务种类的显示名（docs/03 §5.22 任务）。 */
export const HOST_TASK_KIND_LABELS: Record<HostTaskKind, string> = {
  provision: '部署',
  verify: '校验连接',
  restart_panel: '重启面板',
  restart_core: '重启内核',
  upgrade: '升级 / 修复',
  reissue_token: '重签令牌',
  enable_tls: '配置证书',
  diagnose: '查看状态与日志',
  deploy_inbounds: '一键部署入站',
}

/** 任务状态的显示名与颜色（docs/03 §5.22：queued → running → succeeded / failed，queued 可取消）。 */
export const HOST_TASK_STATUS_META: Record<HostTaskStatus, TagMeta> = {
  queued: { label: '排队中', color: 'blue' },
  running: { label: '执行中', color: 'processing' },
  succeeded: { label: '成功', color: 'green' },
  failed: { label: '失败', color: 'red' },
  canceled: { label: '已取消' },
}

/** 任务步骤的显示名；键是契约里 HostTask.step 的取值，空串（还没开始）不在表里。 */
const HOST_TASK_STEP_LABELS: Record<string, string> = {
  connect: '连接主机',
  detect: '检测环境',
  install: '安装面板',
  adopt: '接管面板',
  register: '接入平台',
  run: '执行动作',
  dns: '设置解析',
  issue: '申请证书',
  install_cert: '安装证书',
  ports: '检查端口',
  inbounds: '建入站',
  firewall: '放行防火墙',
  xray: '确认 Xray',
  node_groups: '加进节点组',
}

const STEP_NOT_STARTED = '未开始'

/** 步骤的显示名：空串为“未开始”，不认识的步骤原样显示。 */
export function hostStepLabel(step: string): string {
  if (step === '') {
    return STEP_NOT_STARTED
  }
  return HOST_TASK_STEP_LABELS[step] ?? step
}

/** 失败或不能部署的原因大类（docs/03 §5.22）：每条都写明管理员下一步怎么办。 */
const HOST_ERROR_TEXT: Record<Exclude<HostErrorCode, ''>, string> = {
  unsupported_os: '目标机不是受支持的发行版，请换一台系统受支持的机器',
  no_systemd: '目标机没有 systemd，面板无法作为服务管理；请换用带 systemd 的系统',
  no_root: '登录用户不是 root，也不能免密 sudo；请换用 root 凭据，或给该用户配好免密 sudo 后重新部署',
  nonstandard_install: '机器上已有 Docker 或非标准路径安装的面板，平台不接管；请改成标准安装，或在“服务器”里手工接入',
  listen_local: '面板只监听本机，平台连不上；请在面板设置里改为监听公网地址后重新部署',
  version_too_old: '已有面板的版本低于 v3.0，平台不接管；请先在机器上把面板升级后重新部署',
  download_failed: '目标机下载不到安装包（多为连不上 GitHub）；请检查目标机的网络或代理后重新部署',
  panel_unreachable: '面板端口从平台连不上，请检查防火墙或云厂商安全组后重新部署',
  auth_failed: 'SSH 认证失败；请核对凭据里的用户名、私钥或密码，以及目标机是否允许这种登录方式',
  unreachable: 'SSH 连不上；请检查地址、SSH 端口，以及目标机防火墙是否放行平台的 IP',
  host_key_changed: '主机密钥与上次记录的不一致；确认机器确实重装过后，先重置主机密钥',
  name_taken: '服务器名已被占用；请把主机名称改成别的，再重新部署',
  dns_failed: 'DNS 服务商拒绝了改解析的请求；请检查 DNS 账号的令牌权限与根域名，下一轮检查会自动重试，也可以手动重做“配置证书”',
  cert_failed: '主机上的 acme.sh 申请证书失败，失败摘要与日志里有它的报错；排除问题后等下一轮自动重试，或手动重做“配置证书”',
  interrupted: '平台重启时任务被中断；重新发起即可',
  command_failed: '命令执行失败，详见日志；排除问题后重新发起',
  port_in_use: '所选端口已被主机上的程序或面板已有入站占用；换个端口再部署',
  xray_down: '入站建好了，但面板的 Xray 没在运行，节点暂不可用；看日志里的报错，必要时删掉新建的入站',
}

/** 原因大类的中文说明；空串（没有原因）返回空串，表里没有的大类（后端先于前端加了新值）原样显示。 */
export function hostErrorText(code: HostErrorCode): string {
  if (code === '') {
    return ''
  }
  return HOST_ERROR_TEXT[code] ?? code
}

const ACTIVE_STATUSES: readonly HostTaskStatus[] = ['queued', 'running']

/** 任务是否还没结束（排队中或执行中）；没有任务算已结束。未结束时页面才轮询、主机才不能再发起动作。 */
export function isHostTaskActive(task: Pick<HostTask, 'status'> | null | undefined): boolean {
  return task !== null && task !== undefined && ACTIVE_STATUSES.includes(task.status)
}

/** SSH 认证方式的显示名。 */
export const SSH_AUTH_TYPE_LABELS: Record<SshAuthType, string> = {
  key: '私钥',
  password: '密码',
}

/** DNS 服务商的显示名（docs/03 §5.22 DNS 账号）。 */
export const DNS_PROVIDER_LABELS: Record<DnsProviderType, string> = {
  cloudflare: 'Cloudflare',
}

const ISO_DATE_LEN = 10
const CERT_NOT_ISSUED = '还没申请到证书'

/**
 * 主机上平台申请的证书的到期说明：有到期时间取它的日期（UTC），没有则说明还没申请到。
 * @param notAfter 契约里的 certNotAfter
 */
export function certExpiryText(notAfter: string | null): string {
  return notAfter === null ? CERT_NOT_ISSUED : `证书到期 ${notAfter.slice(0, ISO_DATE_LEN)}`
}

/** 连接测试里“已有面板”的显示名。 */
export const PANEL_STATE_LABELS: Record<HostDetection['panelState'], string> = {
  none: '没有面板',
  standard: '标准安装的面板',
  nonstandard: '非标准安装的面板',
}

/** 连接测试里部署会走哪条路；空串（不能部署）不在表里。 */
export const DEPLOY_PLAN_LABELS: Record<Exclude<HostDetection['plan'], ''>, string> = {
  install: '安装',
  adopt: '接管',
}

const PLAIN_HTTP_PREFIX = 'http://'

/** 面板地址是不是明文 HTTP（接管了没有证书的面板，docs/03 §5.22 接管）。 */
export function isPlainHttpUrl(baseUrl: string): boolean {
  return baseUrl.toLowerCase().startsWith(PLAIN_HTTP_PREFIX)
}

/** 明文 HTTP 面板的警告标签与说明。 */
export const PLAIN_HTTP_TAG = '明文 HTTP'
export const PLAIN_HTTP_WARNING = '面板令牌以明文传输，建议在 SSH 运维里“配置证书”'
