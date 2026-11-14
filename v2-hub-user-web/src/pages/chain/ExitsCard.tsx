import { useState, type FormEvent } from 'react'
import { api, csrfHeader, toApiError, type ChainExit, type ChainOverview } from '../../api/client'
import { useToast } from '../../components/toast-context'
import { Alert, Badge, Button, Card, Confirm, Empty, Field, Modal, TextAreaField } from '../../components/ui'
import { useCopy } from '../../hooks/use-copy'
import { mutate } from '../../hooks/use-remote'
import { MAX_CHAIN_NAME_LEN, MAX_EXIT_LINK_LEN, isValidChainName } from './labels'
import { useI18n } from '../../i18n/i18n-context'

const LINK_ROWS = 4
const NEW_EXIT_KEY = 'new'
const NO_ROUTES = 0

interface ExitFormModalProps {
  /** null = 添加；否则编辑这个出口。 */
  exit: ChainExit | null
  isOpen: boolean
  onClose: () => void
  onSaved: () => void
}

/** 添加或编辑我的出口：添加时链接必填；编辑时链接留空表示不换。 */
function ExitFormModal({ exit, isOpen, onClose, onSaved }: ExitFormModalProps) {
  const { t } = useI18n()
  const [name, setName] = useState(exit?.name ?? '')
  const [link, setLink] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)
  const isEditing = exit !== null
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
      if (exit === null) {
        return api.POST('/api/user/chain/exits', { params: csrfHeader(), body: { link: trimmedLink, name: name.trim() } })
      }
      const body = trimmedLink === '' ? { name: name.trim() } : { name: name.trim(), link: trimmedLink }
      return api.PUT('/api/user/chain/exits/{id}', { params: { ...csrfHeader(), path: { id: exit.id } }, body })
    })
    setIsBusy(false)
    if (err !== null) {
      setError(err.message)
      return
    }
    onSaved()
  }

  return (
    <Modal title={t(isEditing ? 'chain.editExit' : 'chain.addExit')} isOpen={isOpen} onClose={onClose}>
      {error !== null && <Alert tone="plain">{error}</Alert>}
      <form onSubmit={(e) => void handleSubmit(e)} noValidate>
        <TextAreaField
          label={t(isEditing ? 'chain.newLink' : 'chain.exitLink')}
          autoComplete="off"
          spellCheck={false}
          rows={LINK_ROWS}
          maxLength={MAX_EXIT_LINK_LEN}
          value={link}
          onChange={(e) => setLink(e.target.value)}
          hint={t('chain.linkHint')}
        />
        <Field
          label={t(isEditing ? 'chain.name' : 'chain.nameOptional')}
          autoComplete="off"
          maxLength={MAX_CHAIN_NAME_LEN}
          value={name}
          onChange={(e) => setName(e.target.value)}
          error={isValidChainName(name) ? null : t('chain.badName', { max: MAX_CHAIN_NAME_LEN })}
          hint={isEditing ? undefined : t('chain.exitNameHint')}
        />
        <div className="hub-actions">
          <Button type="submit" variant="primary" isBusy={isBusy} disabled={!canSubmit}>
            {t('common.save')}
          </Button>
          <Button variant="outline" onClick={onClose}>
            {t('common.cancel')}
          </Button>
        </div>
      </form>
    </Modal>
  )
}

interface ExitItemProps {
  exit: ChainExit
  isWritable: boolean
  onEdit: () => void
  onDelete: () => void
}

/** 一个出口：原链接属于凭据，默认不取，点“查看链接”才单独请求，只留在本组件内存里。被管理员停用的标出来。 */
function ExitItem({ exit, isWritable, onEdit, onDelete }: ExitItemProps) {
  const { t } = useI18n()
  const toast = useToast()
  const copy = useCopy()
  const [link, setLink] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const reveal = async () => {
    setIsBusy(true)
    try {
      const res = await api.GET('/api/user/chain/exits/{id}/link', { params: { path: { id: exit.id } } })
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
        <span className="hub-shared-name">{exit.name}</span>
        <Badge tone="light">{exit.protocol}</Badge>
        {!exit.isEnabled && <Badge tone="danger">{t('chain.exitDisabledByAdmin')}</Badge>}
      </div>
      <p className="hub-field-hint">
        <span className="hub-mono">
          {exit.address}:{exit.port}
        </span>
        {t('chain.refCount', { n: exit.routeCount })}
      </p>
      {link !== null && <div className="hub-chain-link hub-mono">{link}</div>}
      <div className="hub-actions">
        {link === null ? (
          <Button variant="outline" isBusy={isBusy} onClick={() => void reveal()}>
            {t('chain.viewLink')}
          </Button>
        ) : (
          <>
            <Button variant="outline" onClick={() => void copy(link, t('chain.linkCopied'))}>
              {t('common.copy')}
            </Button>
            <Button variant="outline" onClick={() => setLink(null)}>
              {t('chain.hide')}
            </Button>
          </>
        )}
        {isWritable && (
          <Button variant="outline" onClick={onEdit}>
            {t('chain.edit')}
          </Button>
        )}
        <Button variant="danger" onClick={onDelete}>
          {t('common.delete')}
        </Button>
      </div>
    </div>
  )
}

/** “我的出口”卡（docs/03 §5.21）：只能作落地；套餐不允许时只读，但仍可删除。 */
export function ExitsCard({ overview, onChanged }: { overview: ChainOverview; onChanged: () => void }) {
  const { t } = useI18n()
  const toast = useToast()
  const [editing, setEditing] = useState<ChainExit | null>(null)
  const [isAdding, setIsAdding] = useState(false)
  const [deleting, setDeleting] = useState<ChainExit | null>(null)
  const [isDeleting, setIsDeleting] = useState(false)
  const exits = overview.exits
  const isFull = exits.length >= overview.maxExits

  const closeForm = () => {
    setIsAdding(false)
    setEditing(null)
  }
  const handleSaved = () => {
    closeForm()
    toast.show(t('chain.saved'), 'success')
    onChanged()
  }

  const handleDelete = async () => {
    if (deleting === null) {
      return
    }
    setIsDeleting(true)
    const err = await mutate(() => api.DELETE('/api/user/chain/exits/{id}', { params: { ...csrfHeader(), path: { id: deleting.id } } }))
    setIsDeleting(false)
    if (err !== null) {
      toast.show(err.message, 'error')
      return
    }
    setDeleting(null)
    toast.show(t('chain.deleted'), 'success')
    onChanged()
  }

  const addButton = (
    <Button variant="primary" disabled={!overview.exitAllowed || isFull} onClick={() => setIsAdding(true)}>
      {t('chain.add')}
    </Button>
  )

  return (
    <Card title={t('chain.exitsTitle', { count: exits.length, max: overview.maxExits })} extra={addButton}>
      {!overview.exitAllowed && <Alert tone="info">{t('chain.exitsNotAllowed')}</Alert>}
      {overview.exitAllowed && isFull && <Alert tone="info">{t('chain.exitsFull', { max: overview.maxExits })}</Alert>}
      {exits.length === 0 && <Empty>{t('chain.exitsEmpty')}</Empty>}
      {exits.map((x) => (
        <ExitItem key={x.id} exit={x} isWritable={overview.exitAllowed} onEdit={() => setEditing(x)} onDelete={() => setDeleting(x)} />
      ))}
      {/* 每次打开都重新挂载，表单状态不残留上一次的输入 */}
      {(isAdding || editing !== null) && <ExitFormModal key={editing?.id ?? NEW_EXIT_KEY} exit={editing} isOpen onClose={closeForm} onSaved={handleSaved} />}
      <Confirm title={t('chain.deleteExit')} isOpen={deleting !== null} isBusy={isDeleting} confirmText={t('common.delete')} onConfirm={() => void handleDelete()} onClose={() => setDeleting(null)}>
        <p>{t('chain.deleteExitConfirm', { name: deleting?.name ?? '' })}</p>
        {deleting !== null && deleting.routeCount > NO_ROUTES && <Alert tone="warning">{t('chain.deleteExitCascade', { n: deleting.routeCount })}</Alert>}
      </Confirm>
    </Card>
  )
}
