import { useState } from 'react'
import { api, csrfHeader, type ChainOverview, type ChainRule } from '../../api/client'
import { useToast } from '../../components/toast-context'
import { Alert, Badge, Button, Card, Confirm, Empty, PageHeader } from '../../components/ui'
import { mutate, useRemote } from '../../hooks/use-remote'
import { CustomNodesCard } from './CustomNodesCard'
import { chainStatusLabel, isChainOk } from './labels'
import { CreateRuleModal, RenameRuleModal } from './RuleModals'
import { ServerRulesCard } from './ServerRulesCard'
import { useI18n, type MessageKey } from '../../i18n/i18n-context'

const EXIT_KIND_CUSTOM: ChainRule['exit']['kind'] = 'custom'

const loadChain = (signal: AbortSignal) => api.GET('/api/user/chain', { signal })

interface RulesCardProps {
  overview: ChainOverview
  onChanged: () => void
}

/** 规则表：名称、入口、落地、状态、启用开关（即时保存）、改名、删除（docs/03 §5.18）。 */
function RulesCard({ overview, onChanged }: RulesCardProps) {
  const { t } = useI18n()
  const toast = useToast()
  const [isCreating, setIsCreating] = useState(false)
  const [renaming, setRenaming] = useState<ChainRule | null>(null)
  const [deleting, setDeleting] = useState<ChainRule | null>(null)
  const [busyId, setBusyId] = useState<number | null>(null)
  const rules = overview.rules
  const isFull = rules.length >= overview.maxRules
  const hasNodes = overview.nodes.length > 0

  const toggle = async (rule: ChainRule) => {
    setBusyId(rule.id)
    const err = await mutate(() =>
      api.PUT('/api/user/chain/rules/{id}', { params: { ...csrfHeader(), path: { id: rule.id } }, body: { name: rule.name, isEnabled: !rule.isEnabled } }),
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
    const err = await mutate(() => api.DELETE('/api/user/chain/rules/{id}', { params: { ...csrfHeader(), path: { id: deleting.id } } }))
    setBusyId(null)
    if (err !== null) {
      toast.show(err.message, 'error')
      return
    }
    setDeleting(null)
    toast.show(t('chain.deleted'), 'success')
    onChanged()
  }

  const handleSaved = (message: string) => {
    setIsCreating(false)
    setRenaming(null)
    toast.show(message, 'success')
    onChanged()
  }

  const createButton = (
    <Button variant="primary" disabled={!overview.allowed || isFull || !hasNodes} onClick={() => setIsCreating(true)}>
      {t('chain.newRule')}
    </Button>
  )

  return (
    <Card title={t('chain.clientTitle', { count: rules.length, max: overview.maxRules })} extra={createButton}>
      {!overview.allowed && overview.serverAllowed && <Alert tone="info">{t('chain.clientNotAllowed')}</Alert>}
      {overview.allowed && isFull && <Alert tone="info">{t('chain.full', { max: overview.maxRules })}</Alert>}
      {overview.allowed && !hasNodes && <Alert tone="info">{t('chain.noNodes')}</Alert>}
      {rules.length === 0 ? (
        <Empty>{t('chain.emptyRules')}</Empty>
      ) : (
        <div className="hub-table-wrap">
          <table className="hub-table">
            <thead>
              <tr>
                <th>{t('chain.colName')}</th>
                <th>{t('chain.colEntry')}</th>
                <th>{t('chain.colExit')}</th>
                <th>{t('chain.colStatus')}</th>
                <th>{t('chain.colEnabled')}</th>
                <th>{t('chain.colActions')}</th>
              </tr>
            </thead>
            <tbody>
              {rules.map((r) => (
                <tr key={r.id}>
                  <td>{r.displayName}</td>
                  <td>{r.entry.name}</td>
                  <td>
                    {r.exit.name}
                    {r.exit.kind === EXIT_KIND_CUSTOM && (
                      <>
                        {' '}
                        <Badge tone="info">{t('chain.custom')}</Badge>
                      </>
                    )}
                    {r.exit.protocol !== '' && <span className="hub-field-hint"> · {r.exit.protocol}</span>}
                  </td>
                  <td>
                    {/* 停用的规则不进订阅，通畅与否不再有意义，只显示已停用 */}
                    {r.isEnabled ? <Badge tone={isChainOk(r) ? 'success' : 'danger'}>{chainStatusLabel(t, r)}</Badge> : <Badge tone="light">{t('chain.disabled')}</Badge>}
                  </td>
                  <td>
                    <Button
                      variant={r.isEnabled ? 'outline' : 'primary'}
                      isBusy={busyId === r.id}
                      disabled={!overview.allowed}
                      aria-pressed={r.isEnabled}
                      onClick={() => void toggle(r)}
                    >
                      {t(r.isEnabled ? 'chain.disable' : 'chain.enable')}
                    </Button>
                  </td>
                  <td>
                    <div className="hub-actions">
                      <Button variant="outline" disabled={!overview.allowed} onClick={() => setRenaming(r)}>
                        {t('chain.rename')}
                      </Button>
                      <Button variant="danger" onClick={() => setDeleting(r)}>
                        {t('common.delete')}
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {isCreating && <CreateRuleModal overview={overview} isOpen onClose={() => setIsCreating(false)} onCreated={() => handleSaved(t('chain.created'))} />}
      {renaming !== null && <RenameRuleModal key={renaming.id} rule={renaming} onClose={() => setRenaming(null)} onSaved={() => handleSaved(t('chain.saved'))} />}
      <Confirm title={t('chain.deleteRule')} isOpen={deleting !== null} isBusy={busyId !== null} confirmText={t('common.delete')} onConfirm={() => void handleDelete()} onClose={() => setDeleting(null)}>
        <p>{t('chain.deleteRuleConfirm', { name: deleting?.displayName ?? '' })}</p>
      </Confirm>
    </Card>
  )
}

/** 说明卡的条目，按显示顺序。 */
const NOTE_KEYS: readonly MessageKey[] = ['chain.note1', 'chain.note2', 'chain.note3', 'chain.note4', 'chain.note5', 'chain.note6']

/** 说明卡：哪些订阅格式带链路、流量怎么计、平台不探测连通性（Ruling-36 – Ruling-38）。 */
function NotesCard() {
  const { t } = useI18n()
  return (
    <Card title={t('chain.notesTitle')}>
      <ul className="hub-notes">
        {NOTE_KEYS.map((key) => (
          <li key={key}>{t(key)}</li>
        ))}
      </ul>
    </Card>
  )
}

/** 链式代理（docs/03 §5.18、§5.19，docs/06 §5 M6 / M7）：一个接口取全，任何写操作成功后整体重取。 */
export function ChainPage() {
  const { t } = useI18n()
  const chain = useRemote<ChainOverview>(loadChain)
  const overview = chain.data

  return (
    <>
      <PageHeader title={t('menu.chain')} />
      {chain.error !== null && <Alert tone="plain">{chain.error.message}</Alert>}
      {overview !== null && !overview.allowed && !overview.serverAllowed && (
        <Alert tone="warning">{t('chain.planNotAllowed')}</Alert>
      )}
      {overview !== null && (
        <>
          <RulesCard overview={overview} onChanged={chain.reload} />
          <ServerRulesCard overview={overview} onChanged={chain.reload} />
          <div className="hub-grid-2">
            <CustomNodesCard overview={overview} onChanged={chain.reload} />
            <NotesCard />
          </div>
        </>
      )}
    </>
  )
}
