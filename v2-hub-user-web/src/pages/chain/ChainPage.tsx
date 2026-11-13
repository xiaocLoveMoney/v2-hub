import { useState } from 'react'
import { api, csrfHeader, type ChainOverview, type ChainRule } from '../../api/client'
import { useToast } from '../../components/toast-context'
import { Alert, Badge, Button, Card, Confirm, Empty, PageHeader } from '../../components/ui'
import { mutate, useRemote } from '../../hooks/use-remote'
import { CustomNodesCard } from './CustomNodesCard'
import { chainStatusLabel, isChainOk } from './labels'
import { CreateRuleModal, RenameRuleModal } from './RuleModals'

const EXIT_KIND_CUSTOM: ChainRule['exit']['kind'] = 'custom'

const loadChain = (signal: AbortSignal) => api.GET('/api/user/chain', { signal })

interface RulesCardProps {
  overview: ChainOverview
  onChanged: () => void
}

/** 规则表：名称、入口、落地、状态、启用开关（即时保存）、改名、删除（docs/03 §5.18）。 */
function RulesCard({ overview, onChanged }: RulesCardProps) {
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
    toast.show('已删除', 'success')
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
      新建规则
    </Button>
  )

  return (
    <Card title={`链式规则（${rules.length} / ${overview.maxRules}）`} extra={createButton}>
      {overview.allowed && isFull && <Alert tone="info">已达 {overview.maxRules} 条上限，删除不用的再新建。</Alert>}
      {overview.allowed && !hasNodes && <Alert tone="info">当前没有可用的节点，暂时无法新建规则。</Alert>}
      {rules.length === 0 ? (
        <Empty>还没有规则。新建一条，订阅里就会多出一个“入口 → 落地”节点。</Empty>
      ) : (
        <div className="hub-table-wrap">
          <table className="hub-table">
            <thead>
              <tr>
                <th>名称</th>
                <th>入口</th>
                <th>落地</th>
                <th>状态</th>
                <th>启用</th>
                <th>操作</th>
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
                        <Badge tone="info">自有</Badge>
                      </>
                    )}
                    {r.exit.protocol !== '' && <span className="hub-field-hint"> · {r.exit.protocol}</span>}
                  </td>
                  <td>
                    {/* 停用的规则不进订阅，通畅与否不再有意义，只显示已停用 */}
                    {r.isEnabled ? <Badge tone={isChainOk(r) ? 'success' : 'danger'}>{chainStatusLabel(r)}</Badge> : <Badge tone="light">已停用</Badge>}
                  </td>
                  <td>
                    <Button
                      variant={r.isEnabled ? 'outline' : 'primary'}
                      isBusy={busyId === r.id}
                      disabled={!overview.allowed}
                      aria-pressed={r.isEnabled}
                      onClick={() => void toggle(r)}
                    >
                      {r.isEnabled ? '停用' : '启用'}
                    </Button>
                  </td>
                  <td>
                    <div className="hub-actions">
                      <Button variant="outline" disabled={!overview.allowed} onClick={() => setRenaming(r)}>
                        改名
                      </Button>
                      <Button variant="danger" onClick={() => setDeleting(r)}>
                        删除
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {isCreating && <CreateRuleModal overview={overview} isOpen onClose={() => setIsCreating(false)} onCreated={() => handleSaved('已创建')} />}
      {renaming !== null && <RenameRuleModal key={renaming.id} rule={renaming} onClose={() => setRenaming(null)} onSaved={() => handleSaved('已保存')} />}
      <Confirm title="删除规则" isOpen={deleting !== null} isBusy={busyId !== null} confirmText="删除" onConfirm={() => void handleDelete()} onClose={() => setDeleting(null)}>
        <p>确定删除“{deleting?.displayName}”？订阅里的这个节点会在客户端下次更新时消失。</p>
      </Confirm>
    </Card>
  )
}

/** 说明卡：哪些订阅格式带链路、流量怎么计、平台不探测连通性（Ruling-36 – Ruling-38）。 */
function NotesCard() {
  return (
    <Card title="说明">
      <ul className="hub-notes">
        <li>客户端先连入口节点，再经入口连到落地节点出网；配置全部写在订阅里，平台不转发流量。</li>
        <li>Mihomo、sing-box、Xray JSON 订阅会带上链路，Base64 订阅不带；链路只出现在手动选择组，不参与自动测速。</li>
        <li>状态“通畅”表示配置与授权成立，平台不探测连通性，也不连接你填写的地址；能否连通以客户端实测为准。</li>
        <li>阻断的规则会保留但不进订阅，原因消除后（如续上支持的套餐、节点恢复）自动恢复通畅。</li>
        <li>落地是平台节点时，同一份流量在入口与落地各计一次；落地是自有节点时只计入口。</li>
      </ul>
    </Card>
  )
}

/** 链式代理（docs/03 §5.18、docs/06 §5 M6）：一个接口取全，任何写操作成功后整体重取。 */
export function ChainPage() {
  const chain = useRemote<ChainOverview>(loadChain)
  const overview = chain.data

  return (
    <>
      <PageHeader title="链式代理" />
      {chain.error !== null && <Alert tone="plain">{chain.error.message}</Alert>}
      {overview !== null && !overview.allowed && <Alert tone="warning">当前套餐不支持链式代理：已有的规则处于阻断状态、不会出现在订阅里，仍可查看与删除。</Alert>}
      {overview !== null && (
        <>
          <RulesCard overview={overview} onChanged={chain.reload} />
          <div className="hub-grid-2">
            <CustomNodesCard overview={overview} onChanged={chain.reload} />
            <NotesCard />
          </div>
        </>
      )}
    </>
  )
}
