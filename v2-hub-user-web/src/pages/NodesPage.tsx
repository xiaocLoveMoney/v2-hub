import { Copy, Globe } from 'lucide-react'
import { QRCodeSVG } from 'qrcode.react'
import { useState } from 'react'
import { api, toApiError, type UserNode, type UserNodeGroup } from '../api/client'
import { useSession } from '../auth/session-context'
import { Alert, Badge, Button, Card, Empty, Modal, PageHeader } from '../components/ui'
import { regionFlag } from '../format'
import { useCopy } from '../hooks/use-copy'
import { useRemote } from '../hooks/use-remote'
import { useI18n } from '../i18n/i18n-context'

const QR_SIZE = 200
const STATUS_ACTIVE = 'active'

const loadNodes = (signal: AbortSignal) => api.GET('/api/user/nodes', { signal })

interface LinkState {
  node: UserNode
  link: string | null
  error: string | null
}

function NodeCard({ node, canOpen, onOpen }: { node: UserNode; canOpen: boolean; onOpen: (n: UserNode) => void }) {
  const { t } = useI18n()
  const flag = regionFlag(node.region)
  return (
    <Card>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
        <div style={{ minWidth: 0 }}>
          <div style={{ fontWeight: 700, color: 'var(--hub-heading)', fontSize: 15 }}>
            <span className={`hub-dot${node.isServerOnline ? ' hub-dot-on' : ''}`} title={t(node.isServerOnline ? 'nodes.online' : 'nodes.offline')} />
            {flag === '' ? <Globe size={14} style={{ marginRight: 6, verticalAlign: -2 }} /> : <span style={{ marginRight: 6 }}>{flag}</span>}
            {node.name}
          </div>
          <div className="hub-actions" style={{ marginTop: 10 }}>
            <Badge tone="light">{node.protocol}</Badge>
            {node.region !== '' && <Badge>{node.region}</Badge>}
            <span className="hub-muted">{t('nodes.onlineClients', { n: node.onlineClients })}</span>
            <span className="hub-muted">{t('nodes.load', { value: node.serverLoad === null || node.serverLoad === undefined ? t('common.dash') : `${node.serverLoad.toFixed(0)}%` })}</span>
          </div>
        </div>
        {canOpen && (
          <Button variant="outline" onClick={() => onOpen(node)}>
            {t('nodes.view')}
          </Button>
        )}
      </div>
    </Card>
  )
}

/** 节点列表（docs/06 §5）：按节点组分段；点"查看"取该节点的分享链接与二维码（凭据，按需加载）。 */
export function NodesPage() {
  const { t } = useI18n()
  const { session } = useSession()
  const copy = useCopy()
  const groups = useRemote<UserNodeGroup[]>(loadNodes)
  const [detail, setDetail] = useState<LinkState | null>(null)
  const canOpen = session?.user.status === STATUS_ACTIVE

  const openNode = async (node: UserNode) => {
    setDetail({ node, link: null, error: null })
    try {
      const { data, error } = await api.GET('/api/user/nodes/{id}/link', { params: { path: { id: node.id } } })
      const link = data?.data?.link
      if (error !== undefined || link === undefined) {
        setDetail({ node, link: null, error: toApiError(error).message })
        return
      }
      setDetail({ node, link, error: null })
    } catch (err) {
      reportError(err)
      setDetail({ node, link: null, error: toApiError(err).message })
    }
  }

  return (
    <>
      <PageHeader title={t('menu.nodes')} />
      {groups.error !== null && <Alert tone="plain">{groups.error.message}</Alert>}
      {groups.data !== null && groups.data.length === 0 && (
        <Card>
          <Empty>{t('nodes.empty')}</Empty>
        </Card>
      )}
      {(groups.data ?? []).map((g) => (
        <section key={g.id}>
          <h2 className="hub-section-title">{g.name}</h2>
          <div className="hub-grid-2">
            {g.nodes.map((n) => (
              <NodeCard key={n.id} node={n} canOpen={canOpen} onOpen={(node) => void openNode(node)} />
            ))}
          </div>
        </section>
      ))}
      <Modal title={detail?.node.name ?? ''} isOpen={detail !== null} onClose={() => setDetail(null)}>
        {detail?.error !== null && detail?.error !== undefined && <Alert tone="plain">{detail.error}</Alert>}
        {detail !== null && detail.link === null && detail.error === null && <div className="hub-empty">{t('common.loading')}</div>}
        {detail !== null && detail.link !== null && (
          <>
            <div style={{ display: 'grid', placeItems: 'center', marginBottom: 16 }}>
              <QRCodeSVG value={detail.link} size={QR_SIZE} />
            </div>
            <div className="hub-copy-row">
              <input className="hub-input hub-mono" readOnly aria-label={t('nodes.link')} value={detail.link} />
              <Button variant="primary" onClick={() => void copy(detail.link ?? '', t('nodes.linkCopied'))}>
                <Copy size={14} />
                {t('common.copy')}
              </Button>
            </div>
            <p className="hub-field-hint">{t('nodes.linkHint')}</p>
          </>
        )}
      </Modal>
    </>
  )
}
