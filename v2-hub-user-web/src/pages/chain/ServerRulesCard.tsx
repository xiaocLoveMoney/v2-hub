import { useEffect, useState, type FormEvent } from 'react'
import { api, csrfHeader, type ChainOverview, type ServerChainRule } from '../../api/client'
import { useToast } from '../../components/toast-context'
import { Alert, Badge, Button, Card, Confirm, Empty, Modal, SelectField } from '../../components/ui'
import { mutate } from '../../hooks/use-remote'
import { serverRuleState } from './labels'

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
    <Modal title="新建服务端规则" isOpen onClose={onClose}>
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <form onSubmit={(e) => void handleSubmit(e)} noValidate>
        <SelectField
          label="入口节点"
          value={entryId}
          onChange={(e) => {
            setEntryId(e.target.value)
            if (e.target.value === exitId) {
              setExitId('')
            }
          }}
          hint="你连这个节点的全部流量都会改从落地出网；每个入口只能有一条"
        >
          <option value="">请选择</option>
          {entries.map((n) => (
            <option key={n.id} value={n.id}>
              {n.name}
            </option>
          ))}
        </SelectField>
        <SelectField label="落地节点" value={exitId} onChange={(e) => setExitId(e.target.value)} hint="由入口服务器转发到这里出网；hysteria 类节点不能作落地">
          <option value="">请选择</option>
          {exits.map((n) => (
            <option key={n.id} value={n.id}>
              {n.name}
            </option>
          ))}
        </SelectField>
        {entries.length === 0 && <Alert tone="info">没有可作入口的节点：入口所在面板须支持服务端链式，且每个入口只能有一条规则。</Alert>}
        <div className="hub-actions">
          <Button type="submit" variant="primary" isBusy={isBusy} disabled={entryId === '' || exitId === ''}>
            创建
          </Button>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
        </div>
      </form>
    </Modal>
  )
}

/** 服务端规则表（docs/03 §5.19）：入口、落地、状态（阻断 / 已停用 / 生效 / 下发中 / 下发失败）、启停、删除。 */
export function ServerRulesCard({ overview, onChanged }: { overview: ChainOverview; onChanged: () => void }) {
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
    toast.show('已删除', 'success')
    onChanged()
  }

  const createButton = (
    <Button variant="primary" disabled={!overview.serverAllowed || isFull} onClick={() => setIsCreating(true)}>
      新建服务端规则
    </Button>
  )

  return (
    <Card title={`服务端链式（${rules.length} / ${overview.maxServerRules}）`} extra={createButton}>
      {!overview.serverAllowed && overview.allowed && <Alert tone="info">当前套餐不支持服务端链式；已有的规则处于阻断状态、不会生效，仍可删除。</Alert>}
      {hasPending && <Alert tone="info">下发中：同一台服务器两次下发至少间隔 60 秒，通常一两分钟内生效，本页会自动刷新。</Alert>}
      {rules.length === 0 ? (
        <Empty>服务端链式不改订阅：给某个入口节点配上落地后，你连这个入口的流量就由服务器转发到落地出网。</Empty>
      ) : (
        <div className="hub-table-wrap">
          <table className="hub-table">
            <thead>
              <tr>
                <th>入口</th>
                <th>落地</th>
                <th>状态</th>
                <th>启用</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {rules.map((r) => {
                const state = serverRuleState(r)
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
                        {r.isEnabled ? '停用' : '启用'}
                      </Button>
                    </td>
                    <td>
                      <Button variant="danger" onClick={() => setDeleting(r)}>
                        删除
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
            toast.show('已创建，正在下发', 'success')
            onChanged()
          }}
        />
      )}
      <Confirm title="删除服务端规则" isOpen={deleting !== null} isBusy={busyId !== null} confirmText="删除" onConfirm={() => void handleDelete()} onClose={() => setDeleting(null)}>
        <p>确定删除“{deleting?.entry.name} → {deleting?.exit.name}”？删除后连这个入口会恢复直接出网（下一次下发后生效）。</p>
      </Confirm>
    </Card>
  )
}
