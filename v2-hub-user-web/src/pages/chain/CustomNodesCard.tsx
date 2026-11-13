import { useState, type FormEvent } from 'react'
import { api, csrfHeader, toApiError, type ChainOverview, type CustomNode } from '../../api/client'
import { useToast } from '../../components/toast-context'
import { Alert, Badge, Button, Card, Confirm, Empty, Field, Modal, TextAreaField } from '../../components/ui'
import { useCopy } from '../../hooks/use-copy'
import { mutate } from '../../hooks/use-remote'
import { MAX_CHAIN_NAME_LEN, MAX_CUSTOM_LINK_LEN, isValidChainName } from './labels'

const LINK_HINT = '粘贴 vless:// vmess:// trojan:// ss:// 分享链接，或 socks5://[用户名:口令@]地址:端口'
const LINK_ROWS = 4
const NEW_NODE_KEY = 'new'

interface NodeFormModalProps {
  /** null = 添加；否则编辑这个节点。 */
  node: CustomNode | null
  isOpen: boolean
  onClose: () => void
  onSaved: () => void
}

/** 添加或编辑自有落地节点：添加时链接必填；编辑时链接留空表示不换。 */
function NodeFormModal({ node, isOpen, onClose, onSaved }: NodeFormModalProps) {
  const [name, setName] = useState(node?.name ?? '')
  const [link, setLink] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)
  const isEditing = node !== null
  const trimmedLink = link.trim()
  const isNameValid = isValidChainName(name) && (!isEditing || name.trim() !== '')
  const canSubmit = isNameValid && (isEditing || trimmedLink !== '')

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (!canSubmit) {
      return
    }
    setIsBusy(true)
    setError(null)
    const err = await mutate(() => {
      if (node === null) {
        return api.POST('/api/user/chain/custom-nodes', { params: csrfHeader(), body: { link: trimmedLink, name: name.trim() } })
      }
      const body = trimmedLink === '' ? { name: name.trim() } : { name: name.trim(), link: trimmedLink }
      return api.PUT('/api/user/chain/custom-nodes/{id}', { params: { ...csrfHeader(), path: { id: node.id } }, body })
    })
    setIsBusy(false)
    if (err !== null) {
      setError(err.message)
      return
    }
    onSaved()
  }

  return (
    <Modal title={isEditing ? '编辑自有节点' : '添加自有节点'} isOpen={isOpen} onClose={onClose}>
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <form onSubmit={(e) => void handleSubmit(e)} noValidate>
        <TextAreaField
          label={isEditing ? '新链接（留空不换）' : '节点链接'}
          autoComplete="off"
          spellCheck={false}
          rows={LINK_ROWS}
          maxLength={MAX_CUSTOM_LINK_LEN}
          value={link}
          onChange={(e) => setLink(e.target.value)}
          hint={LINK_HINT}
        />
        <Field
          label={isEditing ? '名称' : '名称（可选）'}
          autoComplete="off"
          maxLength={MAX_CHAIN_NAME_LEN}
          value={name}
          onChange={(e) => setName(e.target.value)}
          error={isValidChainName(name) ? null : `名称不能超过 ${MAX_CHAIN_NAME_LEN} 个字符，且不能含换行`}
          hint={isEditing ? undefined : '留空则取链接里的名称'}
        />
        <div className="hub-actions">
          <Button type="submit" variant="primary" isBusy={isBusy} disabled={!canSubmit}>
            保存
          </Button>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
        </div>
      </form>
    </Modal>
  )
}

interface NodeItemProps {
  node: CustomNode
  isWritable: boolean
  onEdit: () => void
  onDelete: () => void
}

/** 一个自有节点：原链接属于凭据，默认不取，点“查看链接”才单独请求，只留在本组件内存里。 */
function NodeItem({ node, isWritable, onEdit, onDelete }: NodeItemProps) {
  const toast = useToast()
  const copy = useCopy()
  const [link, setLink] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const reveal = async () => {
    setIsBusy(true)
    try {
      const res = await api.GET('/api/user/chain/custom-nodes/{id}/link', { params: { path: { id: node.id } } })
      if (res.error !== undefined || res.data?.data === undefined) {
        toast.show(toApiError(res.error).message, 'error')
        return
      }
      setLink(res.data.data.link)
    } catch (err) {
      reportError(err)
      toast.show(toApiError(err).message, 'error')
    } finally {
      setIsBusy(false)
    }
  }

  return (
    <div className="hub-shared-item">
      <div className="hub-chain-node-head">
        <span className="hub-shared-name">{node.name}</span>
        <Badge tone="light">{node.protocol}</Badge>
      </div>
      <p className="hub-field-hint">
        <span className="hub-mono">
          {node.address}:{node.port}
        </span>
        {` · 被 ${node.ruleCount} 条规则引用`}
      </p>
      {link !== null && <div className="hub-chain-link hub-mono">{link}</div>}
      <div className="hub-actions">
        {link === null ? (
          <Button variant="outline" isBusy={isBusy} onClick={() => void reveal()}>
            查看链接
          </Button>
        ) : (
          <>
            <Button variant="outline" onClick={() => void copy(link, '链接已复制')}>
              复制
            </Button>
            <Button variant="outline" onClick={() => setLink(null)}>
              隐藏
            </Button>
          </>
        )}
        {isWritable && (
          <Button variant="outline" onClick={onEdit}>
            编辑
          </Button>
        )}
        <Button variant="danger" onClick={onDelete}>
          删除
        </Button>
      </div>
    </div>
  )
}

/** 自有落地节点卡（docs/03 §5.18）：只能作落地；套餐不允许时只读，但仍可删除。 */
export function CustomNodesCard({ overview, onChanged }: { overview: ChainOverview; onChanged: () => void }) {
  const toast = useToast()
  const [editing, setEditing] = useState<CustomNode | null>(null)
  const [isAdding, setIsAdding] = useState(false)
  const [deleting, setDeleting] = useState<CustomNode | null>(null)
  const [isDeleting, setIsDeleting] = useState(false)
  const nodes = overview.customNodes
  const isFull = nodes.length >= overview.maxCustomNodes

  const closeForm = () => {
    setIsAdding(false)
    setEditing(null)
  }
  const handleSaved = () => {
    closeForm()
    toast.show('已保存', 'success')
    onChanged()
  }

  const handleDelete = async () => {
    if (deleting === null) {
      return
    }
    setIsDeleting(true)
    const err = await mutate(() => api.DELETE('/api/user/chain/custom-nodes/{id}', { params: { ...csrfHeader(), path: { id: deleting.id } } }))
    setIsDeleting(false)
    if (err !== null) {
      toast.show(err.message, 'error')
      return
    }
    setDeleting(null)
    toast.show('已删除', 'success')
    onChanged()
  }

  const addButton = (
    <Button variant="primary" disabled={!overview.customNodeAllowed || isFull} onClick={() => setIsAdding(true)}>
      添加
    </Button>
  )

  return (
    <Card title={`自有落地节点（${nodes.length} / ${overview.maxCustomNodes}）`} extra={addButton}>
      {!overview.customNodeAllowed && <Alert tone="info">当前套餐不支持自有落地节点，已有的只能查看与删除，引用它们的规则处于阻断状态。</Alert>}
      {overview.customNodeAllowed && isFull && <Alert tone="info">已达 {overview.maxCustomNodes} 个上限，删除不用的再添加。</Alert>}
      {nodes.length === 0 && <Empty>还没有自有节点。自己的服务器可以添加进来，作为链路的落地出口。</Empty>}
      {nodes.map((n) => (
        <NodeItem key={n.id} node={n} isWritable={overview.customNodeAllowed} onEdit={() => setEditing(n)} onDelete={() => setDeleting(n)} />
      ))}
      {/* 每次打开都重新挂载，表单状态不残留上一次的输入 */}
      {(isAdding || editing !== null) && <NodeFormModal key={editing?.id ?? NEW_NODE_KEY} node={editing} isOpen onClose={closeForm} onSaved={handleSaved} />}
      <Confirm title="删除自有节点" isOpen={deleting !== null} isBusy={isDeleting} confirmText="删除" onConfirm={() => void handleDelete()} onClose={() => setDeleting(null)}>
        <p>确定删除“{deleting?.name}”？</p>
        {deleting !== null && deleting.ruleCount > 0 && <Alert tone="warning">有 {deleting.ruleCount} 条规则以它为落地，会一并删除。</Alert>}
      </Confirm>
    </Card>
  )
}
