import { Copy, Globe } from 'lucide-react'
import { QRCodeSVG } from 'qrcode.react'
import { useState } from 'react'
import { api, toApiError, type UserNode, type UserNodeGroup } from '../api/client'
import { useSession } from '../auth/session-context'
import { Alert, Badge, Button, Card, Empty, Modal, PageHeader } from '../components/ui'
import { regionFlag } from '../format'
import { useCopy } from '../hooks/use-copy'
import { useRemote } from '../hooks/use-remote'

const QR_SIZE = 200
const STATUS_ACTIVE = 'active'

const loadNodes = (signal: AbortSignal) => api.GET('/api/user/nodes', { signal })

interface LinkState {
  node: UserNode
  link: string | null
  error: string | null
}

function NodeCard({ node, canOpen, onOpen }: { node: UserNode; canOpen: boolean; onOpen: (n: UserNode) => void }) {
  const flag = regionFlag(node.region)
  return (
    <Card>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
        <div style={{ minWidth: 0 }}>
          <div style={{ fontWeight: 700, color: 'var(--hub-heading)', fontSize: 15 }}>
            <span className={`hub-dot${node.isServerOnline ? ' hub-dot-on' : ''}`} title={node.isServerOnline ? '在线' : '离线'} />
            {flag === '' ? <Globe size={14} style={{ marginRight: 6, verticalAlign: -2 }} /> : <span style={{ marginRight: 6 }}>{flag}</span>}
            {node.name}
          </div>
          <div className="hub-actions" style={{ marginTop: 10 }}>
            <Badge tone="light">{node.protocol}</Badge>
            {node.region !== '' && <Badge>{node.region}</Badge>}
            <span className="hub-muted">在线 {node.onlineClients}</span>
            <span className="hub-muted">负载 {node.serverLoad === null || node.serverLoad === undefined ? '—' : `${node.serverLoad.toFixed(0)}%`}</span>
          </div>
        </div>
        {canOpen && (
          <Button variant="outline" onClick={() => onOpen(node)}>
            查看
          </Button>
        )}
      </div>
    </Card>
  )
}

/** 节点列表（docs/06 §5）：按节点组分段；点"查看"取该节点的分享链接与二维码（凭据，按需加载）。 */
export function NodesPage() {
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
      <PageHeader title="节点列表" />
      {groups.error !== null && <Alert tone="plain">{groups.error.message}</Alert>}
      {groups.data !== null && groups.data.length === 0 && (
        <Card>
          <Empty>当前套餐没有可用节点</Empty>
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
        {detail !== null && detail.link === null && detail.error === null && <div className="hub-empty">加载中…</div>}
        {detail !== null && detail.link !== null && (
          <>
            <div style={{ display: 'grid', placeItems: 'center', marginBottom: 16 }}>
              <QRCodeSVG value={detail.link} size={QR_SIZE} />
            </div>
            <div className="hub-copy-row">
              <input className="hub-input hub-mono" readOnly aria-label="节点链接" value={detail.link} />
              <Button variant="primary" onClick={() => void copy(detail.link ?? '', '已复制节点链接')}>
                <Copy size={14} />
                复制
              </Button>
            </div>
            <p className="hub-field-hint">节点链接含连接凭据，请勿分享给他人。</p>
          </>
        )}
      </Modal>
    </>
  )
}
