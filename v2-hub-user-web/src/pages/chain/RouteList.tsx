import { useState } from 'react'
import { api, csrfHeader, type ChainKind, type ChainOverview, type ChainRoute } from '../../api/client'
import { useToast } from '../../components/toast-context'
import { Alert, Badge, Button, Card, Confirm, Empty } from '../../components/ui'
import { mutate } from '../../hooks/use-remote'
import { useI18n } from '../../i18n/i18n-context'
import { EXIT_KIND_KEYS, KIND_SERVER, entryState, routeState } from './labels'
import { RouteModal } from './RouteModal'

const STATE_PENDING: ChainRoute['entries'][number]['state'] = 'pending'
const STEP_UP = -1
const STEP_DOWN = 1
const NEW_ROUTE_KEY = 'new'

interface RouteItemProps {
  route: ChainRoute
  isFirst: boolean
  isLast: boolean
  isWritable: boolean
  isBusy: boolean
  onMove: (step: number) => void
  onToggle: () => void
  onEdit: () => void
  onDelete: () => void
}

/** 一条规则：序号、起飞节点（每个带自己的状态）、落地与规则状态，以及调顺序、启停、编辑、删除。 */
function RouteItem({ route, isFirst, isLast, isWritable, isBusy, onMove, onToggle, onEdit, onDelete }: RouteItemProps) {
  const { t } = useI18n()
  const state = routeState(t, route)
  return (
    <li className="hub-route">
      <div className="hub-route-head">
        <span className="hub-route-position">{t('chain.position', { n: route.position })}</span>
        <span className="hub-route-name">{route.displayName}</span>
        <Badge tone={state.tone}>{state.label}</Badge>
      </div>
      <div className="hub-route-line">
        <span className="hub-route-label">{t('chain.entries')}</span>
        <span className="hub-route-entries">
          {route.entries.map((e) => {
            const s = entryState(t, e)
            return (
              <span key={e.nodeId} className="hub-route-entry">
                {e.name}
                {s !== null && (
                  <>
                    {' '}
                    <Badge tone={s.tone}>{s.label}</Badge>
                  </>
                )}
              </span>
            )
          })}
        </span>
      </div>
      <div className="hub-route-line">
        <span className="hub-route-label">{t('chain.exit')}</span>
        <span>
          {route.exit.name} <Badge tone="light">{t(EXIT_KIND_KEYS[route.exit.kind])}</Badge>
          {route.exit.protocol !== '' && <span className="hub-field-hint"> · {route.exit.protocol}</span>}
        </span>
      </div>
      <div className="hub-actions">
        <Button variant="outline" disabled={isFirst || !isWritable || isBusy} aria-label={t('chain.moveUpOf', { n: route.position })} onClick={() => onMove(STEP_UP)}>
          {t('chain.moveUp')}
        </Button>
        <Button variant="outline" disabled={isLast || !isWritable || isBusy} aria-label={t('chain.moveDownOf', { n: route.position })} onClick={() => onMove(STEP_DOWN)}>
          {t('chain.moveDown')}
        </Button>
        <Button variant={route.isEnabled ? 'outline' : 'primary'} disabled={!isWritable || isBusy} aria-pressed={route.isEnabled} onClick={onToggle}>
          {t(route.isEnabled ? 'chain.disable' : 'chain.enable')}
        </Button>
        <Button variant="outline" disabled={!isWritable || isBusy} onClick={onEdit}>
          {t('chain.edit')}
        </Button>
        <Button variant="danger" disabled={isBusy} onClick={onDelete}>
          {t('common.delete')}
        </Button>
      </div>
    </li>
  )
}

interface RouteListProps {
  overview: ChainOverview
  kind: ChainKind
  onChanged: () => void
}

/**
 * 某一类型的规则列表（docs/06 §5 M9）：顺序即优先级，上移 / 下移点一次提交一次；任何写操作成功后由上层整体重取。
 * 套餐没开这类链式时只能查看与删除。
 */
export function RouteList({ overview, kind, onChanged }: RouteListProps) {
  const { t } = useI18n()
  const toast = useToast()
  const [isCreating, setIsCreating] = useState(false)
  const [editing, setEditing] = useState<ChainRoute | null>(null)
  const [deleting, setDeleting] = useState<ChainRoute | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const isServer = kind === KIND_SERVER
  const routes = isServer ? overview.serverRoutes : overview.clientRoutes
  const max = isServer ? overview.maxServerRoutes : overview.maxClientRoutes
  const isAllowed = isServer ? overview.serverAllowed : overview.allowed
  const isFull = routes.length >= max
  const hasNodes = overview.nodes.length > 0
  const hasPending = isServer && routes.some((r) => r.entries.some((e) => e.state === STATE_PENDING))

  /** 执行一次写操作：失败提示错误，成功提示并让上层重取。 */
  const run = async (action: Parameters<typeof mutate>[0], okText: string | null) => {
    setIsBusy(true)
    const err = await mutate(action)
    setIsBusy(false)
    if (err !== null) {
      toast.show(err.message, 'error')
      return false
    }
    if (okText !== null) {
      toast.show(okText, 'success')
    }
    onChanged()
    return true
  }

  const move = (index: number, step: number) => {
    const ids = routes.map((r) => r.id)
    const target = index + step
    if (target < 0 || target >= ids.length) {
      return
    }
    ;[ids[index], ids[target]] = [ids[target], ids[index]]
    void run(() => api.PUT('/api/user/chain/routes/order', { params: csrfHeader(), body: { kind, ids } }), null)
  }

  const toggle = (route: ChainRoute) =>
    void run(
      () =>
        api.PUT('/api/user/chain/routes/{id}', {
          params: { ...csrfHeader(), path: { id: route.id } },
          body: {
            entryNodeIds: route.entries.map((e) => e.nodeId),
            exitKind: route.exit.kind,
            exitId: route.exit.id,
            name: route.name,
            isEnabled: !route.isEnabled,
          },
        }),
      null,
    )

  const handleDelete = async () => {
    if (deleting === null) {
      return
    }
    const isDone = await run(() => api.DELETE('/api/user/chain/routes/{id}', { params: { ...csrfHeader(), path: { id: deleting.id } } }), t('chain.deleted'))
    if (isDone) {
      setDeleting(null)
    }
  }

  const handleSaved = (okText: string) => {
    setIsCreating(false)
    setEditing(null)
    toast.show(okText, 'success')
    onChanged()
  }

  const addButton = (
    <Button variant="primary" disabled={!isAllowed || isFull || !hasNodes} onClick={() => setIsCreating(true)}>
      {t('chain.newRule')}
    </Button>
  )

  return (
    <Card title={t(isServer ? 'chain.tabServer' : 'chain.tabClient', { count: routes.length, max })} extra={addButton}>
      {!isAllowed && <Alert tone="info">{t(isServer ? 'chain.serverNotAllowed' : 'chain.clientNotAllowed')}</Alert>}
      {isAllowed && isFull && <Alert tone="info">{t('chain.full', { max })}</Alert>}
      {isAllowed && !hasNodes && <Alert tone="info">{t('chain.noNodes')}</Alert>}
      {hasPending && <Alert tone="info">{t('chain.serverPending')}</Alert>}
      {routes.length === 0 ? (
        <Empty>{t(isServer ? 'chain.serverEmpty' : 'chain.clientEmpty')}</Empty>
      ) : (
        <>
          <p className="hub-field-hint">{t(isServer ? 'chain.serverOrderHint' : 'chain.clientOrderHint')}</p>
          <ol className="hub-route-list">
            {routes.map((r, i) => (
              <RouteItem
                key={r.id}
                route={r}
                isFirst={i === 0}
                isLast={i === routes.length - 1}
                isWritable={isAllowed}
                isBusy={isBusy}
                onMove={(step) => move(i, step)}
                onToggle={() => toggle(r)}
                onEdit={() => setEditing(r)}
                onDelete={() => setDeleting(r)}
              />
            ))}
          </ol>
        </>
      )}
      {/* 每次打开都重新挂载，表单状态不残留上一次的输入 */}
      {(isCreating || editing !== null) && (
        <RouteModal
          key={editing?.id ?? NEW_ROUTE_KEY}
          overview={overview}
          kind={kind}
          route={editing}
          onClose={() => {
            setIsCreating(false)
            setEditing(null)
          }}
          onSaved={() => handleSaved(t(editing === null ? 'chain.created' : 'chain.saved'))}
        />
      )}
      <Confirm title={t('chain.deleteRule')} isOpen={deleting !== null} isBusy={isBusy} confirmText={t('common.delete')} onConfirm={() => void handleDelete()} onClose={() => setDeleting(null)}>
        <p>{t('chain.deleteRuleConfirm', { n: deleting?.position ?? 0, name: deleting?.displayName ?? '' })}</p>
        <p className="hub-field-hint">{t(isServer ? 'chain.deleteServerHint' : 'chain.deleteClientHint')}</p>
      </Confirm>
    </Card>
  )
}
