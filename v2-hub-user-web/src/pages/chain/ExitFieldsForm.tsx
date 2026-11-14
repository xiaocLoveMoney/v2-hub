import { useId } from 'react'
import { Field, SelectField } from '../../components/ui'
import { useI18n, type MessageKey } from '../../i18n/i18n-context'
import {
  EXIT_ALPNS,
  EXIT_FINGERPRINTS,
  EXIT_FLOWS,
  EXIT_NETWORKS,
  EXIT_PROTOCOLS,
  EXIT_SS_METHODS,
  EXIT_VMESS_CIPHERS,
  EXIT_XHTTP_MODES,
  MAX_EXIT_PORT,
  MIN_EXIT_PORT,
  hasFlow,
  hasStream,
  isPlainProxy,
  normalizeExitFields,
  securitiesOf,
  type ExitFieldKey,
  type ExitFields,
  type ExitNetwork,
  type ExitProtocol,
  type ExitSecurity,
} from './exit-link'

/** 协议与传输是专有名词，中英文界面写法相同，不进词条。 */
const PROTOCOL_LABELS: Record<ExitProtocol, string> = {
  vless: 'VLESS',
  vmess: 'VMess',
  trojan: 'Trojan',
  shadowsocks: 'Shadowsocks',
  socks: 'SOCKS5',
  http: 'HTTP',
  https: 'HTTPS',
}
const NETWORK_LABELS: Record<ExitNetwork, string> = {
  tcp: 'RAW (TCP)',
  ws: 'WebSocket',
  grpc: 'gRPC',
  httpupgrade: 'HTTPUpgrade',
  xhttp: 'XHTTP',
}
const SECURITY_LABEL_KEYS: Record<ExitSecurity, MessageKey> = {
  none: 'chain.fSecurityNone',
  tls: 'chain.fSecurityTls',
  reality: 'chain.fSecurityReality',
}
/** 文本字段的长度上限，与后端校验一致（chain_guard.go）。 */
const MAX_HOST_LEN = 255
const MAX_PATH_LEN = 1024
const MAX_CREDENTIAL_LEN = 255
const MAX_SHORT_ID_LEN = 16
const PATH_PLACEHOLDER = '/'
const PORT_DIGITS = 5

interface ExitFieldsFormProps {
  fields: ExitFields
  onChange: (fields: ExitFields) => void
  /** 缺项或格式不对的字段（exitFieldProblems 的结果）。 */
  problems: ReadonlySet<ExitFieldKey>
  /** 是否把缺项标出来：点过一次保存之后才标，免得一打开就满屏报错。 */
  shouldShowProblems: boolean
}

/**
 * 出口的连接参数表单（docs/06 §5 M9 出口弹窗）：字段参照 3x-ui 的出站表单，按协议、传输、安全层显示对应的一组。
 * 受控组件：自己不存状态，每次改动把收拾过的整份字段交回上层。
 */
export function ExitFieldsForm({ fields, onChange, problems, shouldShowProblems }: ExitFieldsFormProps) {
  const { t } = useI18n()
  const securityLabelId = useId()
  const alpnLabelId = useId()

  const set = (patch: Partial<ExitFields>) => onChange(normalizeExitFields({ ...fields, ...patch }))
  const errorOf = (key: ExitFieldKey, messageKey: MessageKey = 'chain.fRequired') =>
    shouldShowProblems && problems.has(key) ? t(messageKey, { min: MIN_EXIT_PORT, max: MAX_EXIT_PORT }) : null
  const toggleAlpn = (value: string) => {
    const has = fields.alpn.includes(value)
    // 保持固定的顺序，不按点击先后
    set({ alpn: EXIT_ALPNS.filter((a) => (a === value ? !has : fields.alpn.includes(a))) })
  }

  const { protocol, network, security } = fields
  const isStream = hasStream(protocol)
  const hasHostPath = isStream && (network === 'ws' || network === 'httpupgrade' || network === 'xhttp')
  const hasTls = isStream && security !== 'none'

  return (
    <>
      <SelectField label={t('chain.fProtocol')} value={protocol} onChange={(e) => set({ protocol: e.target.value as ExitProtocol })}>
        {EXIT_PROTOCOLS.map((p) => (
          <option key={p} value={p}>
            {PROTOCOL_LABELS[p]}
          </option>
        ))}
      </SelectField>
      <div className="hub-exit-row">
        <Field
          label={t('chain.fAddress')}
          autoComplete="off"
          spellCheck={false}
          maxLength={MAX_HOST_LEN}
          value={fields.address}
          onChange={(e) => set({ address: e.target.value })}
          error={errorOf('address')}
        />
        <Field
          label={t('chain.fPort')}
          inputMode="numeric"
          autoComplete="off"
          maxLength={PORT_DIGITS}
          value={fields.port}
          onChange={(e) => set({ port: e.target.value })}
          error={errorOf('port', 'chain.fBadPort')}
        />
      </div>

      {(protocol === 'vless' || protocol === 'vmess') && (
        <Field
          label={t('chain.fId')}
          placeholder="UUID"
          autoComplete="off"
          spellCheck={false}
          maxLength={MAX_CREDENTIAL_LEN}
          value={fields.id}
          onChange={(e) => set({ id: e.target.value })}
          error={errorOf('id')}
        />
      )}
      {protocol === 'vmess' && (
        <SelectField label={t('chain.fCipher')} value={fields.cipher} onChange={(e) => set({ cipher: e.target.value })}>
          {EXIT_VMESS_CIPHERS.map((c) => (
            <option key={c} value={c}>
              {c}
            </option>
          ))}
        </SelectField>
      )}
      {protocol === 'shadowsocks' && (
        <SelectField label={t('chain.fMethod')} value={fields.method} onChange={(e) => set({ method: e.target.value })}>
          {/* 链接里带了下拉没有的加密方式时照样显示出来，由后端决定收不收 */}
          {!(EXIT_SS_METHODS as readonly string[]).includes(fields.method) && <option value={fields.method}>{fields.method}</option>}
          {EXIT_SS_METHODS.map((m) => (
            <option key={m} value={m}>
              {m}
            </option>
          ))}
        </SelectField>
      )}
      {isPlainProxy(protocol) && (
        <Field
          label={t('chain.fUsernameOptional')}
          autoComplete="off"
          spellCheck={false}
          maxLength={MAX_CREDENTIAL_LEN}
          value={fields.username}
          onChange={(e) => set({ username: e.target.value })}
          error={errorOf('username')}
        />
      )}
      {(protocol === 'trojan' || protocol === 'shadowsocks' || isPlainProxy(protocol)) && (
        <Field
          label={t(isPlainProxy(protocol) ? 'chain.fPassphraseOptional' : 'chain.fPassphrase')}
          autoComplete="off"
          spellCheck={false}
          maxLength={MAX_CREDENTIAL_LEN}
          value={fields.passphrase}
          onChange={(e) => set({ passphrase: e.target.value })}
          error={errorOf('passphrase')}
        />
      )}

      {protocol === 'https' && (
        <>
          <Field
            label={t('chain.fSni')}
            autoComplete="off"
            spellCheck={false}
            maxLength={MAX_HOST_LEN}
            value={fields.sni}
            onChange={(e) => set({ sni: e.target.value })}
            hint={t('chain.fSniProxyHint')}
          />
          <label className="hub-exit-check hub-exit-check-block">
            <input type="checkbox" checked={fields.isInsecure} onChange={(e) => set({ isInsecure: e.target.checked })} />
            <span>{t('chain.fInsecure')}</span>
          </label>
        </>
      )}

      {isStream && (
        <SelectField label={t('chain.fNetwork')} value={network} onChange={(e) => set({ network: e.target.value as ExitNetwork })}>
          {EXIT_NETWORKS.map((n) => (
            <option key={n} value={n}>
              {NETWORK_LABELS[n]}
            </option>
          ))}
        </SelectField>
      )}
      {hasHostPath && (
        <>
          <Field
            label={t('chain.fHost')}
            autoComplete="off"
            spellCheck={false}
            maxLength={MAX_HOST_LEN}
            value={fields.host}
            onChange={(e) => set({ host: e.target.value })}
          />
          <Field
            label={t('chain.fPath')}
            placeholder={PATH_PLACEHOLDER}
            autoComplete="off"
            spellCheck={false}
            maxLength={MAX_PATH_LEN}
            value={fields.path}
            onChange={(e) => set({ path: e.target.value })}
          />
        </>
      )}
      {isStream && network === 'xhttp' && (
        <SelectField label={t('chain.fMode')} value={fields.mode} onChange={(e) => set({ mode: e.target.value })}>
          {EXIT_XHTTP_MODES.map((m) => (
            <option key={m} value={m}>
              {m}
            </option>
          ))}
        </SelectField>
      )}
      {isStream && network === 'grpc' && (
        <Field
          label={t('chain.fServiceName')}
          autoComplete="off"
          spellCheck={false}
          maxLength={MAX_HOST_LEN}
          value={fields.serviceName}
          onChange={(e) => set({ serviceName: e.target.value })}
        />
      )}

      {isStream && (
        <div className="hub-field">
          <span className="hub-field-label" id={securityLabelId}>
            {t('chain.fSecurity')}
          </span>
          <div className="hub-seg hub-exit-seg" role="group" aria-labelledby={securityLabelId}>
            {securitiesOf(protocol).map((s) => (
              <button
                key={s}
                type="button"
                className={`hub-seg-item${s === security ? ' is-active' : ''}`}
                aria-pressed={s === security}
                onClick={() => set({ security: s })}
              >
                {t(SECURITY_LABEL_KEYS[s])}
              </button>
            ))}
          </div>
        </div>
      )}
      {hasFlow(fields) && (
        <SelectField label={t('chain.fFlow')} value={fields.flow} onChange={(e) => set({ flow: e.target.value })}>
          <option value="">{t('chain.fNone')}</option>
          {EXIT_FLOWS.map((f) => (
            <option key={f} value={f}>
              {f}
            </option>
          ))}
        </SelectField>
      )}
      {hasTls && (
        <>
          <Field
            label={t('chain.fSni')}
            autoComplete="off"
            spellCheck={false}
            maxLength={MAX_HOST_LEN}
            value={fields.sni}
            onChange={(e) => set({ sni: e.target.value })}
            error={errorOf('sni')}
          />
          <SelectField label={t('chain.fFingerprint')} value={fields.fingerprint} onChange={(e) => set({ fingerprint: e.target.value })}>
            {/* REALITY 必须有指纹，所以不给“不指定” */}
            {security === 'tls' && <option value="">{t('chain.fNone')}</option>}
            {EXIT_FINGERPRINTS.map((f) => (
              <option key={f} value={f}>
                {f}
              </option>
            ))}
          </SelectField>
        </>
      )}
      {isStream && security === 'tls' && (
        <>
          <div className="hub-field">
            <span className="hub-field-label" id={alpnLabelId}>
              {t('chain.fAlpn')}
            </span>
            <div className="hub-exit-checks" role="group" aria-labelledby={alpnLabelId}>
              {EXIT_ALPNS.map((a) => (
                <label key={a} className="hub-exit-check">
                  <input type="checkbox" checked={fields.alpn.includes(a)} onChange={() => toggleAlpn(a)} />
                  <span>{a}</span>
                </label>
              ))}
            </div>
          </div>
          {/* vmess 的分享链接带不了这一项 */}
          {protocol !== 'vmess' && (
            <label className="hub-exit-check hub-exit-check-block">
              <input type="checkbox" checked={fields.isInsecure} onChange={(e) => set({ isInsecure: e.target.checked })} />
              <span>{t('chain.fInsecure')}</span>
            </label>
          )}
        </>
      )}
      {isStream && security === 'reality' && (
        <>
          <Field
            label={t('chain.fPublicKey')}
            autoComplete="off"
            spellCheck={false}
            maxLength={MAX_CREDENTIAL_LEN}
            value={fields.publicKey}
            onChange={(e) => set({ publicKey: e.target.value })}
            error={errorOf('publicKey')}
          />
          <Field
            label={t('chain.fShortId')}
            autoComplete="off"
            spellCheck={false}
            maxLength={MAX_SHORT_ID_LEN}
            value={fields.shortId}
            onChange={(e) => set({ shortId: e.target.value })}
          />
          <Field
            label={t('chain.fSpiderX')}
            placeholder={PATH_PLACEHOLDER}
            autoComplete="off"
            spellCheck={false}
            maxLength={MAX_PATH_LEN}
            value={fields.spiderX}
            onChange={(e) => set({ spiderX: e.target.value })}
          />
        </>
      )}
    </>
  )
}
