import { useState, type FormEvent } from 'react'
import { api, csrfHeader, type ChainOverview, type ChainRule } from '../../api/client'
import { Alert, Button, Field, Modal, SelectField } from '../../components/ui'
import { mutate } from '../../hooks/use-remote'
import { MAX_CHAIN_NAME_LEN, isValidChainName } from './labels'

const EXIT_NODE_PREFIX = 'node:'
const EXIT_CUSTOM_PREFIX = 'custom:'
const ARROW = ' → '
const BAD_NAME_MESSAGE = `名称不能超过 ${MAX_CHAIN_NAME_LEN} 个字符，且不能含换行`

interface ExitChoice {
  exitNodeId?: number
  exitCustomNodeId?: number
  name: string
}

/** 把落地下拉的值（node:ID / custom:ID）还原成接口字段与显示名；没选时返回 null。 */
function parseExit(value: string, overview: ChainOverview): ExitChoice | null {
  if (value.startsWith(EXIT_NODE_PREFIX)) {
    const id = Number(value.slice(EXIT_NODE_PREFIX.length))
    const node = overview.nodes.find((n) => n.id === id)
    return node === undefined ? null : { exitNodeId: id, name: node.name }
  }
  if (value.startsWith(EXIT_CUSTOM_PREFIX)) {
    const id = Number(value.slice(EXIT_CUSTOM_PREFIX.length))
    const node = overview.customNodes.find((n) => n.id === id)
    return node === undefined ? null : { exitCustomNodeId: id, name: node.name }
  }
  return null
}

interface CreateRuleModalProps {
  overview: ChainOverview
  isOpen: boolean
  onClose: () => void
  onCreated: () => void
}

/**
 * 新建规则：入口取本人可用的平台节点；落地分“平台节点”（排除所选入口与不能作落地的协议）与“自有节点”两组。
 * 名称可空，空时订阅里用“入口 → 落地”（docs/03 §5.18）。
 */
export function CreateRuleModal({ overview, isOpen, onClose, onCreated }: CreateRuleModalProps) {
  const [entryId, setEntryId] = useState('')
  const [exitValue, setExitValue] = useState('')
  const [name, setName] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const entry = overview.nodes.find((n) => String(n.id) === entryId)
  const exit = parseExit(exitValue, overview)
  const exitNodes = overview.nodes.filter((n) => n.canBeExit && String(n.id) !== entryId)
  const customNodes = overview.customNodeAllowed ? overview.customNodes : []
  const defaultName = entry !== undefined && exit !== null ? `${entry.name}${ARROW}${exit.name}` : '入口 → 落地'
  const isNameValid = isValidChainName(name)

  const reset = () => {
    setEntryId('')
    setExitValue('')
    setName('')
    setError(null)
  }
  const handleClose = () => {
    reset()
    onClose()
  }

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (entry === undefined || exit === null || !isNameValid) {
      return
    }
    setIsBusy(true)
    setError(null)
    const err = await mutate(() =>
      api.POST('/api/user/chain/rules', {
        params: csrfHeader(),
        body: { entryNodeId: entry.id, exitNodeId: exit.exitNodeId, exitCustomNodeId: exit.exitCustomNodeId, name: name.trim(), isEnabled: true },
      }),
    )
    setIsBusy(false)
    if (err !== null) {
      setError(err.message)
      return
    }
    reset()
    onCreated()
  }

  return (
    <Modal title="新建链式规则" isOpen={isOpen} onClose={handleClose}>
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <form onSubmit={(e) => void handleSubmit(e)} noValidate>
        <SelectField
          label="入口节点"
          value={entryId}
          onChange={(e) => {
            setEntryId(e.target.value)
            // 入口换成了原来选的落地时清掉落地，避免入口 = 落地
            if (exitValue === `${EXIT_NODE_PREFIX}${e.target.value}`) {
              setExitValue('')
            }
          }}
          hint="客户端先连到这里"
        >
          <option value="">请选择</option>
          {overview.nodes.map((n) => (
            <option key={n.id} value={n.id}>
              {n.name}
            </option>
          ))}
        </SelectField>
        <SelectField label="落地节点" value={exitValue} onChange={(e) => setExitValue(e.target.value)} hint="再经入口连到这里出网；hysteria 类节点不能作落地">
          <option value="">请选择</option>
          <optgroup label="平台节点">
            {exitNodes.map((n) => (
              <option key={n.id} value={`${EXIT_NODE_PREFIX}${n.id}`}>
                {n.name}
              </option>
            ))}
          </optgroup>
          {customNodes.length > 0 && (
            <optgroup label="自有节点">
              {customNodes.map((n) => (
                <option key={n.id} value={`${EXIT_CUSTOM_PREFIX}${n.id}`}>
                  {n.name}
                </option>
              ))}
            </optgroup>
          )}
        </SelectField>
        <Field
          label="名称（可选）"
          autoComplete="off"
          placeholder={defaultName}
          maxLength={MAX_CHAIN_NAME_LEN}
          value={name}
          onChange={(e) => setName(e.target.value)}
          error={isNameValid ? null : BAD_NAME_MESSAGE}
          hint="订阅里显示的节点名，留空就用“入口 → 落地”"
        />
        <div className="hub-actions">
          <Button type="submit" variant="primary" isBusy={isBusy} disabled={entry === undefined || exit === null || !isNameValid}>
            创建
          </Button>
          <Button variant="outline" onClick={handleClose}>
            取消
          </Button>
        </div>
      </form>
    </Modal>
  )
}

interface RenameRuleModalProps {
  rule: ChainRule | null
  onClose: () => void
  onSaved: () => void
}

/** 改规则名称；清空即恢复默认名“入口 → 落地”。rule 为 null 时不显示。 */
export function RenameRuleModal({ rule, onClose, onSaved }: RenameRuleModalProps) {
  const [name, setName] = useState(rule?.name ?? '')
  const [error, setError] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)
  const isNameValid = isValidChainName(name)

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (rule === null || !isNameValid) {
      return
    }
    setIsBusy(true)
    setError(null)
    const err = await mutate(() =>
      api.PUT('/api/user/chain/rules/{id}', { params: { ...csrfHeader(), path: { id: rule.id } }, body: { name: name.trim(), isEnabled: rule.isEnabled } }),
    )
    setIsBusy(false)
    if (err !== null) {
      setError(err.message)
      return
    }
    onSaved()
  }

  return (
    <Modal title="修改名称" isOpen={rule !== null} onClose={onClose}>
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <form onSubmit={(e) => void handleSubmit(e)} noValidate>
        <Field
          label="名称"
          autoComplete="off"
          placeholder={rule === null ? '' : `${rule.entry.name}${ARROW}${rule.exit.name}`}
          maxLength={MAX_CHAIN_NAME_LEN}
          value={name}
          onChange={(e) => setName(e.target.value)}
          error={isNameValid ? null : BAD_NAME_MESSAGE}
          hint="留空恢复为“入口 → 落地”"
        />
        <div className="hub-actions">
          <Button type="submit" variant="primary" isBusy={isBusy} disabled={!isNameValid}>
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
