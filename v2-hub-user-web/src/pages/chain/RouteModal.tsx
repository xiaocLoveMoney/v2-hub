import { useState, type FormEvent } from 'react'
import { api, csrfHeader, type ChainExitKind, type ChainKind, type ChainOverview, type ChainRoute } from '../../api/client'
import { MultiSelect, type MultiSelectOption } from '../../components/MultiSelect'
import { Alert, Button, Field, Modal, SelectField } from '../../components/ui'
import { mutate } from '../../hooks/use-remote'
import { useI18n, type Translate } from '../../i18n/i18n-context'
import { MAX_CHAIN_NAME_LEN, isValidChainName } from './labels'

const KIND_SERVER: ChainKind = 'server'
const EXIT_SEPARATOR = ':'

interface ExitChoice {
  kind: ChainExitKind
  id: number
}

function exitValue(choice: ExitChoice): string {
  return `${choice.kind}${EXIT_SEPARATOR}${choice.id}`
}

/** 把落地下拉的值（种类:ID）还原成接口字段；没选或认不出时返回 null。 */
function parseExit(value: string): ExitChoice | null {
  const [kind, rawId] = value.split(EXIT_SEPARATOR)
  const id = Number(rawId)
  const isKnownKind = kind === 'node' || kind === 'platform' || kind === 'own'
  return isKnownKind && Number.isInteger(id) && id > 0 ? { kind, id } : null
}

/** 本人其它服务端规则里用作起飞、用作落地的节点（防环，docs/03 §5.19）；selfId 是正在编辑的规则。 */
function serverNodeRoles(overview: ChainOverview, selfId: number | null): { entries: Set<number>; exits: Set<number> } {
  const entries = new Set<number>()
  const exits = new Set<number>()
  for (const r of overview.serverRoutes) {
    if (r.id === selfId) {
      continue
    }
    for (const e of r.entries) {
      entries.add(e.nodeId)
    }
    if (r.exit.kind === 'node') {
      exits.add(r.exit.id)
    }
  }
  return { entries, exits }
}

interface OptionContext {
  overview: ChainOverview
  isServer: boolean
  roles: { entries: Set<number>; exits: Set<number> }
  t: Translate
}

/** 起飞节点的选项：服务端链式里面板不支持的、已作落地的置灰并写明原因；落地选了平台节点时该节点也不能再选。 */
function entryOptions({ overview, isServer, roles, t }: OptionContext, exit: ExitChoice | null): MultiSelectOption[] {
  return overview.nodes.map((n) => {
    const isChosenExit = exit !== null && exit.kind === 'node' && exit.id === n.id
    if (isServer && !n.canBeServerEntry) {
      return { value: n.id, label: n.name, isDisabled: true, reason: t('chain.optNoCore') }
    }
    if (isChosenExit || (isServer && roles.exits.has(n.id))) {
      return { value: n.id, label: n.name, isDisabled: true, reason: t('chain.optIsExit') }
    }
    return { value: n.id, label: n.name }
  })
}

interface RouteModalProps {
  overview: ChainOverview
  kind: ChainKind
  /** null = 新建；否则编辑这条规则。 */
  route: ChainRoute | null
  onClose: () => void
  onSaved: () => void
}

/**
 * 新建或编辑一条链式规则（docs/06 §5 M9）：起飞节点下拉多选，落地出口分组下拉单选（平台出口、我的出口、平台节点）。
 * 不可选的项置灰并写原因；最终校验以后端为准。
 */
export function RouteModal({ overview, kind, route, onClose, onSaved }: RouteModalProps) {
  const { t } = useI18n()
  // 已经不可用的起飞节点不带进表单：提交时后端会拒绝，留着只会让用户改不动
  const [entryIds, setEntryIds] = useState<number[]>(() => {
    const available = new Set(overview.nodes.map((n) => n.id))
    return (route?.entries ?? []).map((e) => e.nodeId).filter((id) => available.has(id))
  })
  const [exitRaw, setExitRaw] = useState(route === null ? '' : exitValue(route.exit))
  const [name, setName] = useState(route?.name ?? '')
  const [error, setError] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const isServer = kind === KIND_SERVER
  const roles = serverNodeRoles(overview, route?.id ?? null)
  const exit = parseExit(exitRaw)
  const context: OptionContext = { overview, isServer, roles, t }
  const platformExits = overview.platformExits.filter((x) => (isServer ? x.allowServer : x.allowClient))
  const ownExits = overview.exitAllowed ? overview.exits : []
  const selectedEntries = new Set(entryIds)
  const isNameValid = isValidChainName(name)
  const canSubmit = entryIds.length > 0 && exit !== null && isNameValid

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (exit === null || !canSubmit) {
      return
    }
    setIsBusy(true)
    setError(null)
    const body = { entryNodeIds: entryIds, exitKind: exit.kind, exitId: exit.id, name: name.trim(), isEnabled: route?.isEnabled ?? true }
    const err = await mutate(() =>
      route === null
        ? api.POST('/api/user/chain/routes', { params: csrfHeader(), body: { ...body, kind } })
        : api.PUT('/api/user/chain/routes/{id}', { params: { ...csrfHeader(), path: { id: route.id } }, body }),
    )
    setIsBusy(false)
    if (err !== null) {
      setError(err.message)
      return
    }
    onSaved()
  }

  const title = route !== null ? t('chain.editTitle') : t(isServer ? 'chain.createTitleServer' : 'chain.createTitleClient')
  return (
    <Modal title={title} isOpen onClose={onClose}>
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <form onSubmit={(e) => void handleSubmit(e)} noValidate>
        <MultiSelect
          label={t('chain.entries')}
          placeholder={t('chain.pickEntries')}
          options={entryOptions(context, exit)}
          value={entryIds}
          onChange={setEntryIds}
          max={overview.maxRouteEntries}
          hint={t(isServer ? 'chain.entriesHintServer' : 'chain.entriesHintClient', { max: overview.maxRouteEntries })}
        />
        <SelectField label={t('chain.exit')} value={exitRaw} onChange={(e) => setExitRaw(e.target.value)} hint={t(isServer ? 'chain.exitHintServer' : 'chain.exitHintClient')}>
          <option value="">{t('chain.pick')}</option>
          {platformExits.length > 0 && (
            <optgroup label={t('chain.exitPlatform')}>
              {platformExits.map((x) => (
                <option key={x.id} value={exitValue({ kind: 'platform', id: x.id })}>
                  {x.name}
                </option>
              ))}
            </optgroup>
          )}
          {ownExits.length > 0 && (
            <optgroup label={t('chain.exitOwn')}>
              {ownExits.map((x) => (
                <option key={x.id} value={exitValue({ kind: 'own', id: x.id })} disabled={!x.isEnabled}>
                  {x.isEnabled ? x.name : `${x.name}（${t('chain.optExitDisabled')}）`}
                </option>
              ))}
            </optgroup>
          )}
          <optgroup label={t('chain.exitNode')}>
            {overview.nodes
              .filter((n) => n.canBeExit)
              .map((n) => {
                const isEntry = selectedEntries.has(n.id) || (isServer && roles.entries.has(n.id))
                return (
                  <option key={n.id} value={exitValue({ kind: 'node', id: n.id })} disabled={isEntry}>
                    {isEntry ? `${n.name}（${t('chain.optIsEntry')}）` : n.name}
                  </option>
                )
              })}
          </optgroup>
        </SelectField>
        <Field
          label={t('chain.nameOptional')}
          autoComplete="off"
          maxLength={MAX_CHAIN_NAME_LEN}
          value={name}
          onChange={(e) => setName(e.target.value)}
          error={isNameValid ? null : t('chain.badName', { max: MAX_CHAIN_NAME_LEN })}
          hint={t('chain.nameHint')}
        />
        <div className="hub-actions">
          <Button type="submit" variant="primary" isBusy={isBusy} disabled={!canSubmit}>
            {route === null ? t('chain.create') : t('common.save')}
          </Button>
          <Button variant="outline" onClick={onClose}>
            {t('common.cancel')}
          </Button>
        </div>
      </form>
    </Modal>
  )
}
