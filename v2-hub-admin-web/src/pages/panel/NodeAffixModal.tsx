import { Alert, App, Button, Space, Table, Typography } from 'antd'
import { useCallback, useState, type Key } from 'react'
import { api, csrfHeader, toApiError, type ApiError, type PageData, type PanelNode } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { FormModal } from '../../components/FormModal'
import { useRemote } from '../../hooks/use-remote'
import { NodeAffixInputs } from './NodeAffixInputs'
import { EMPTY_NODE_AFFIX, MAX_AFFIX_NODES, MAX_NODE_NAME_LEN, affixName, isAffixEmpty, isAffixedNameTooLong, normalizeAffix, type NodeAffix } from './node-affix'

const MODAL_WIDTH = 760
const TABLE_HEIGHT = 360
/** 一页拉取的节点数（与接口 pageSize 上限一致）。 */
const NODE_PAGE_SIZE = 100
const MAX_NODE_PAGES = MAX_AFFIX_NODES / NODE_PAGE_SIZE

const MODAL_TITLE = '批量改节点名'

interface AffixModalBaseProps {
  isOpen: boolean
  onClose: () => void
  /** 改完之后调用。 */
  onDone: () => void
}

interface ServerNodeAffixModalProps extends AffixModalBaseProps {
  serverId: number
}

interface NodeAffixModalProps extends AffixModalBaseProps {
  /** 要改的节点（节点目录里勾选的）。 */
  nodes: PanelNode[]
}

/**
 * 服务器详情页的“批量改节点名”弹窗（docs/03 §5.1，Ruling-80；node:write，由调用方判断）。
 * 列出本服务器的节点，默认全选；填前缀 / 后缀后预览新显示名，提交后给选中的节点统一加上。
 */
export function ServerNodeAffixModal({ serverId, isOpen, onClose, onDone }: ServerNodeAffixModalProps) {
  return (
    <FormModal title={MODAL_TITLE} isOpen={isOpen} onClose={onClose} width={MODAL_WIDTH}>
      {(generation) => <ServerNodeAffixBody key={generation} serverId={serverId} onDone={onDone} />}
    </FormModal>
  )
}

/**
 * 节点目录的“批量改节点名”弹窗（Ruling-80；node:write，由调用方判断）：给勾选的节点统一加前缀 / 后缀。
 */
export function NodeAffixModal({ nodes, isOpen, onClose, onDone }: NodeAffixModalProps) {
  return (
    <FormModal title={MODAL_TITLE} isOpen={isOpen} onClose={onClose} width={MODAL_WIDTH}>
      {(generation) => <NodeAffixForm key={generation} nodes={nodes} total={nodes.length} isLoading={false} loadError={null} onDone={onDone} />}
    </FormModal>
  )
}

/** 逐页拉本服务器的节点，最多拉到批量上限；total 是服务器上的节点总数。 */
async function loadServerNodes(serverId: number, signal: AbortSignal): Promise<{ data?: { data?: PageData<PanelNode> }; error?: unknown }> {
  const items: PanelNode[] = []
  let total = 0
  for (let page = 1; page <= MAX_NODE_PAGES; page++) {
    const res = await api.GET('/api/admin/nodes', { params: { query: { page, pageSize: NODE_PAGE_SIZE, serverId } }, signal })
    if (res.error !== undefined || res.data?.data === undefined) {
      return { error: res.error }
    }
    items.push(...res.data.data.items)
    total = res.data.data.total
    if (items.length >= total || res.data.data.items.length === 0) {
      break
    }
  }
  return { data: { data: { items, total } } }
}

function ServerNodeAffixBody({ serverId, onDone }: Pick<ServerNodeAffixModalProps, 'serverId' | 'onDone'>) {
  const load = useCallback((signal: AbortSignal) => loadServerNodes(serverId, signal), [serverId])
  const nodes = useRemote<PageData<PanelNode>>(load)
  return <NodeAffixForm nodes={nodes.data?.items ?? []} total={nodes.data?.total ?? 0} isLoading={nodes.isLoading} loadError={nodes.error} onDone={onDone} />
}

interface NodeAffixFormProps {
  /** 列出来可勾选的节点。 */
  nodes: PanelNode[]
  /** 来源里的节点总数；比 nodes 多时提示没列全。 */
  total: number
  isLoading: boolean
  loadError: ApiError | null
  onDone: () => void
}

function NodeAffixForm({ nodes: rows, total, isLoading, loadError, onDone }: NodeAffixFormProps) {
  const { message } = App.useApp()
  const [affix, setAffix] = useState<NodeAffix>(EMPTY_NODE_AFFIX)
  // 没动过勾选时为 null，表示全选
  const [pickedIds, setPickedIds] = useState<number[] | null>(null)
  const [error, setError] = useState<ApiError | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const selectedIds = pickedIds ?? rows.map((n) => n.id)
  const selected = new Set(selectedIds)
  const hasAffix = !isAffixEmpty(affix)
  const tooLongCount = rows.filter((n) => selected.has(n.id) && isAffixedNameTooLong(n.displayName, affix)).length
  const canSubmit = hasAffix && selectedIds.length > 0 && selectedIds.length <= MAX_AFFIX_NODES && tooLongCount === 0

  const handleSubmit = async () => {
    setIsBusy(true)
    setError(null)
    try {
      const { data: res, error: apiError } = await api.POST('/api/admin/nodes/affix', {
        params: csrfHeader(),
        body: { nodeIds: selectedIds, ...normalizeAffix(affix) },
      })
      if (apiError !== undefined || res?.data === undefined) {
        setError(toApiError(apiError))
        return
      }
      void message.success(`已修改 ${res.data.updated} 个节点的显示名`)
      onDone()
    } catch (err) {
      reportError(err)
      setError(toApiError(err))
    } finally {
      setIsBusy(false)
    }
  }

  return (
    <>
      <ErrorAlert error={loadError ?? error} />
      {total > rows.length && <Alert type="warning" showIcon className="page-alert" title={`这台服务器有 ${total} 个节点，这里只列出前 ${rows.length} 个`} />}
      {selectedIds.length > MAX_AFFIX_NODES && <Alert type="warning" showIcon className="page-alert" title={`一次最多改 ${MAX_AFFIX_NODES} 个节点`} />}
      <Space orientation="vertical" className="full-width">
        <NodeAffixInputs value={affix} onChange={setAffix} />
        <Typography.Text type="secondary">
          加在现有显示名的两头，只改平台上的名字，不写回面板；用户更新订阅后看到新名字。每提交一次就再加一次。
        </Typography.Text>
        <Table<PanelNode>
          rowKey="id"
          size="small"
          loading={isLoading}
          dataSource={rows}
          pagination={false}
          scroll={{ y: TABLE_HEIGHT }}
          rowSelection={{ selectedRowKeys: selectedIds, onChange: (keys: Key[]) => setPickedIds(keys.map(Number)) }}
          columns={[
            { title: '现有显示名', dataIndex: 'displayName' },
            { title: '地址', key: 'address', render: (_, n) => `${n.address}:${n.port}` },
            {
              title: '新显示名',
              key: 'next',
              render: (_, n) => {
                if (!hasAffix || !selected.has(n.id)) {
                  return <Typography.Text type="secondary">不变</Typography.Text>
                }
                const isTooLong = isAffixedNameTooLong(n.displayName, affix)
                return <Typography.Text type={isTooLong ? 'danger' : undefined}>{affixName(n.displayName, affix)}</Typography.Text>
              },
            },
          ]}
        />
        {tooLongCount > 0 && <Typography.Text type="danger">{`有 ${tooLongCount} 个节点加上后超过 ${MAX_NODE_NAME_LEN} 个字符，缩短前后缀或去掉这些节点`}</Typography.Text>}
        <Button type="primary" loading={isBusy} disabled={!canSubmit} onClick={() => void handleSubmit()}>
          修改 {selectedIds.length} 个节点
        </Button>
      </Space>
    </>
  )
}
