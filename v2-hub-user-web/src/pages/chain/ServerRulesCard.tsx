import { useEffect, useState, type FormEvent } from 'react'
import { api, csrfHeader, type ChainOverview, type ServerChainRule } from '../../api/client'
import { useToast } from '../../components/toast-context'
import { Alert, Badge, Button, Card, Confirm, Empty, Modal, SelectField } from '../../components/ui'
import { mutate } from '../../hooks/use-remote'
import { serverRuleState } from './labels'
import { useI18n } from '../../i18n/i18n-context'

/** 有规则在下发中时自动刷新的间隔（docs/06 §5 M7）。 */
const PENDING_REFRESH_MS = 15_000
const APPLY_PENDING: ServerChainRule['applyStatus'] = 'pending'

interface CreateModalProps {
  overview: ChainOverview
  onClose: () => void
  onCreated: () => void
}

/** 新建服务端规则：入口只列面板支持改内核配置、且还没有服务端规则的节点；落地列能作落地且不同于入口的节点。 */
function CreateServerRuleModal({ overview, onClose, onCreated }: CreateModalProps) {
  const { t } = useI18n()
  const [entryId, setEntryId] = useState('')
  const [exitId, setExitId] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)
  const usedEntries = new Set(overview.serverRules.map((r) => r.entry.nodeId))
  const entries = overview.nodes.filter((n) => n.canBeServerEntry && !usedEntries.has(n.id))
  const exits = overview.nodes.filter((n) => n.canBeExit && String(n.id) !== entryId)

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (entryId === '' || exitId === '') {
      return
    }
    setIsBusy(true)
    setError(null)
    const err = await mutate(() =>
      api.POST('/api/user/chain/server-rules', { params: csrfHeader(), body: { entryNodeId: Number(entryId), exitNodeId: Number(exitId), isEnabled: true } }),
    )
    setIsBusy(false)
    if (err !== null) {
      setError(err.message)
      return
    }
    onCreated()
  }

  return (
    <Modal title={t('chain.serverCreateTitle')} isOpen onClose={onClose}>
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <form onSubmit={(e) => void handleSubmit(e)} noValidate>
        <SelectField
          label={t('chain.entryNode')}
          value={entryId}
          onChange={(e) => {
            setEntryId(e.target.value)
            if (e.target.value === exitId) {
              setExitId('')
            }
          }}
          hint={t('chain.serverEntryHint')}
        >
          <option value="">{t('chain.pick')}</option>
          {entries.map((n) => (
            <option key={n.id} value={n.id}>
              {n.name}
            </option>
          ))}
        </SelectField>
        <SelectField label={t('chain.exitNode')} value={exitId} onChange={(e) => setExitId(e.target.value)} hint={t('chain.serverExitHint')}>
          <option value="">{t('chain.pick')}</option>
          {exits.map((n) => (
            <option key={n.id} value={n.id}>
              {n.name}
            </option>
          ))}
        </SelectField>
        {entries.length === 0 && <Alert tone="info">{t('chain.serverNoEntries')}</Alert>}
        <div className="hub-actions">
          <Button type="submit" variant="primary" isBusy={isBusy} disabled={entryId === '' || exitId === ''}>
            {t('chain.create')}
          </Button>
          <Button variant="outline" onClick={onClose}>
            {t('common.cancel')}
          </Button>
        </div>
      </form>
    </Modal>
  )
}

/** 服务端规则表（docs/03 §5.19）：入口、落地、状态（阻断 / 已停用 / 生效 / 下发中 / 下发失败）、启停、删除。 */
export function ServerRulesCard({ overview, onChanged }: { overview: ChainOverview; onChanged: () => void }) {
  const { t } = useI18n()
  const toast = useToast()
  const [isCreating, setIsCreating] = useState(false)
  const [deleting, setDeleting] = useState<ServerChainRule | null>(null)
  const [busyId, setBusyId] = useState<number | null>(null)
  const rules = overview.serverRules
  const isFull = rules.length >= overview.maxServerRules
  const hasPending = rules.some((r) => r.applyStatus === APPLY_PENDING)

  // 下发是后台按服务器合并进行的，有规则在下发中时定时刷新，直到都有结果
  useEffect(() => {
    if (!hasPending) {
      return undefined
    }
    const timer = window.setInterval(onChanged, PENDING_REFRESH_MS)
    return () => window.clearInterval(timer)
  }, [hasPending, onChanged])

  const toggle = async (rule: ServerChainRule) => {
    setBusyId(rule.id)
    const err = await mutate(() =>
      api.PUT('/api/user/chain/server-rules/{id}', { params: { ...csrfHeader(), path: { id: rule.id } }, body: { isEnabled: !rule.isEnabled } }),
    )
    setBusyId(null)
    if (err !== null) {
      toast.show(err.message, 'error')
      return
    }
    onChanged()
  }

  const handleDelete = async () => {
    if (deleting === null) {
      return
    }
    setBusyId(deleting.id)
    const err = await mutate(() => api.DELETE('/api/user/chain/server-rules/{id}', { params: { ...csrfHeader(), path: { id: deleting.id } } }))
    setBusyId(null)
    if (err !== null) {
      toast.show(err.message, 'error')
      return
    }
    setDeleting(null)
    toast.show(t('chain.deleted'), 'success')
    onChanged()
  }

  const createButton = (
    <Button variant="primary" disabled={!overview.serverAllowed || isFull} onClick={() => setIsCreating(true)}>
      {t('chain.serverNew')}
    </Button>
  )

  return (
    <Card title={t('chain.serverTitle', { count: rules.length, max: overview.maxServerRules })} extra={createButton}>
      {!overview.serverAllowed && overview.allowed && <Alert tone="info">{t('chain.serverNotAllowed')}</Alert>}
      {hasPending && <Alert tone="info">{t('chain.serverPending')}</Alert>}
      {rules.length === 0 ? (
        <Empty>{t('chain.serverEmpty')}</Empty>
      ) : (
        <div className="hub-table-wrap">
          <table className="hub-table">
            <thead>
              <tr>
                <th>{t('chain.colEntry')}</th>
                <th>{t('chain.colExit')}</th>
                <th>{t('chain.colStatus')}</th>
                <th>{t('chain.colEnabled')}</th>
                <th>{t('chain.colActions')}</th>
              </tr>
            </thead>
            <tbody>
              {rules.map((r) => {
                const state = serverRuleState(t, r)
                return (
                  <tr key={r.id}>
                    <td>{r.entry.name}</td>
                    <td>
                      {r.exit.name}
                      {r.exit.protocol !== '' && <span className="hub-field-hint"> · {r.exit.protocol}</span>}
                    </td>
                    <td>
                      <Badge tone={state.tone}>{state.label}</Badge>
                    </td>
                    <td>
                      <Button variant={r.isEnabled ? 'outline' : 'primary'} isBusy={busyId === r.id} disabled={!overview.serverAllowed} aria-pressed={r.isEnabled} onClick={() => void toggle(r)}>
                        {t(r.isEnabled ? 'chain.disable' : 'chain.enable')}
                      </Button>
                    </td>
                    <td>
                      <Button variant="danger" onClick={() => setDeleting(r)}>
                        {t('common.delete')}
                      </Button>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      {isCreating && (
        <CreateServerRuleModal
          overview={overview}
          onClose={() => setIsCreating(false)}
          onCreated={() => {
            setIsCreating(false)
            toast.show(t('chain.serverCreated'), 'success')
            onChanged()
          }}
        />
      )}
      <Confirm title={t('chain.serverDelete')} isOpen={deleting !== null} isBusy={busyId !== null} confirmText={t('common.delete')} onConfirm={() => void handleDelete()} onClose={() => setDeleting(null)}>
        <p>{t('chain.serverDeleteConfirm', { entry: deleting?.entry.name ?? '', exit: deleting?.exit.name ?? '' })}</p>
      </Confirm>
    </Card>
  )
}
