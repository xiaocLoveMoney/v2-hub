import { useState } from 'react'
import { api, csrfHeader, type ChainExit, type ChainExitKind, type ChainNodeOption, type ChainOverview, type ChainPlatformExit } from '../../api/client'
import { useToast } from '../../components/toast-context'
import { Alert, Badge, Button, Card, Confirm, Empty } from '../../components/ui'
import { useCopy } from '../../hooks/use-copy'
import { mutate, request } from '../../hooks/use-remote'
import { useI18n } from '../../i18n/i18n-context'
import { ExitModal } from './ExitModal'
import { ExitTestPanel } from './ExitTestPanel'

const NEW_EXIT_KEY = 'new'
const NO_ROUTES = 0
const EXIT_KIND_PLATFORM: ChainExitKind = 'platform'

interface ExitItemProps {
  exit: ChainExit
  /** 可作延迟测试起点的节点。 */
  testNodes: ChainNodeOption[]
  isWritable: boolean
  onEdit: () => void
  onDelete: () => void
}

/** 一个出口：原链接属于凭据，默认不取，点“查看链接”才单独请求，只留在本组件内存里。被管理员停用的标出来。 */
function ExitItem({ exit, testNodes, isWritable, onEdit, onDelete }: ExitItemProps) {
  const { t } = useI18n()
  const toast = useToast()
  const copy = useCopy()
  const [link, setLink] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)
  const [isTesting, setIsTesting] = useState(false)

  const reveal = async () => {
    setIsBusy(true)
    try {
      const result = await request(() => api.GET('/api/user/chain/exits/{id}/link', { params: { path: { id: exit.id } } }))
      if (result.error !== null) {
        toast.show(result.error.message, 'error')
        return
      }
      setLink(result.data.link)
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
          <Button variant="outline" disabled={!exit.isEnabled} aria-expanded={isTesting} onClick={() => setIsTesting((v) => !v)}>
            {t('chain.test')}
          </Button>
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
      {isTesting && <ExitTestPanel exit={exit} nodes={testNodes} />}
    </div>
  )
}

/** 每个平台出口被本人几条规则引用：总览里的平台出口不带引用数，按两类规则数出来。 */
function countPlatformRefs(overview: ChainOverview): Map<number, number> {
  const counts = new Map<number, number>()
  for (const route of [...overview.serverRoutes, ...overview.clientRoutes]) {
    if (route.exit.kind === EXIT_KIND_PLATFORM) {
      counts.set(route.exit.id, (counts.get(route.exit.id) ?? NO_ROUTES) + 1)
    }
  }
  return counts
}

/**
 * 一个平台出口（管理员添加并分配给本人，docs/03 §5.21）：只读、整行置灰。
 * 地址与链接后端不返回，所以没有查看链接、测试、编辑、删除，地址处显示占位。
 */
function PlatformExitItem({ exit, routeCount }: { exit: ChainPlatformExit; routeCount: number }) {
  const { t } = useI18n()
  return (
    <div className="hub-shared-item hub-chain-exit-readonly">
      <div className="hub-chain-node-head">
        <span className="hub-shared-name">{exit.name}</span>
        <Badge tone="light">{exit.protocol}</Badge>
        <Badge tone="light">{t('chain.platformProvided')}</Badge>
        {exit.allowServer && <Badge tone="light">{t('chain.platformForServer')}</Badge>}
        {exit.allowClient && <Badge tone="light">{t('chain.platformForClient')}</Badge>}
      </div>
      <p className="hub-field-hint">
        {t('chain.platformAddressHidden')}
        {t('chain.refCount', { n: routeCount })}
      </p>
    </div>
  )
}

/** “平台提供”一段：本人可用的平台出口，让人看得到管理员已经分配了哪些；没有时整段不显示。 */
function PlatformExits({ overview }: { overview: ChainOverview }) {
  const { t } = useI18n()
  const exits = overview.platformExits
  if (exits.length === 0) {
    return null
  }
  const refs = countPlatformRefs(overview)
  return (
    <section className="hub-chain-platform-exits" aria-label={t('chain.platformExitsTitle', { count: exits.length })}>
      <h5 className="hub-chain-subtitle">{t('chain.platformExitsTitle', { count: exits.length })}</h5>
      <p className="hub-field-hint">{t('chain.platformExitsHint')}</p>
      {exits.map((x) => (
        <PlatformExitItem key={x.id} exit={x} routeCount={refs.get(x.id) ?? NO_ROUTES} />
      ))}
    </section>
  )
}

/** “我的出口”卡（docs/03 §5.21）：只能作落地；套餐不允许时只读，但仍可删除。后面附本人可用的平台出口（只读）。 */
export function ExitsCard({ overview, onChanged }: { overview: ChainOverview; onChanged: () => void }) {
  const { t } = useI18n()
  const toast = useToast()
  const [editing, setEditing] = useState<ChainExit | null>(null)
  const [isAdding, setIsAdding] = useState(false)
  const [deleting, setDeleting] = useState<ChainExit | null>(null)
  const [isDeleting, setIsDeleting] = useState(false)
  const exits = overview.exits
  const isFull = exits.length >= overview.maxExits
  // 起点要由面板去连出口，所以只列面板支持的节点（与能作服务端起飞的是同一批）
  const testNodes = overview.nodes.filter((n) => n.canBeServerEntry)

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
      {exits.length === 0 && overview.platformExits.length === 0 && <Empty>{t('chain.exitsEmpty')}</Empty>}
      {exits.map((x) => (
        <ExitItem key={x.id} exit={x} testNodes={testNodes} isWritable={overview.exitAllowed} onEdit={() => setEditing(x)} onDelete={() => setDeleting(x)} />
      ))}
      <PlatformExits overview={overview} />
      {/* 每次打开都重新挂载，表单状态不残留上一次的输入 */}
      {(isAdding || editing !== null) && <ExitModal key={editing?.id ?? NEW_EXIT_KEY} exit={editing} isOpen onClose={closeForm} onSaved={handleSaved} />}
      <Confirm title={t('chain.deleteExit')} isOpen={deleting !== null} isBusy={isDeleting} confirmText={t('common.delete')} onConfirm={() => void handleDelete()} onClose={() => setDeleting(null)}>
        <p>{t('chain.deleteExitConfirm', { name: deleting?.name ?? '' })}</p>
        {deleting !== null && deleting.routeCount > NO_ROUTES && <Alert tone="warning">{t('chain.deleteExitCascade', { n: deleting.routeCount })}</Alert>}
      </Confirm>
    </Card>
  )
}
