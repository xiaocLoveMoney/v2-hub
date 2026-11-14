import { PlusOutlined } from '@ant-design/icons'
import { App, Button, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Switch, Tag, Typography, type TableProps } from 'antd'
import { useCallback, useState } from 'react'
import {
  api,
  csrfHeader,
  toApiError,
  type AdminChainExit,
  type AdminChainExitInput,
  type ChainExitOwner,
  type NodeGroup,
} from '../../api/client'
import { useSession } from '../../auth/session-context'
import { ErrorAlert } from '../../components/ErrorAlert'
import { PagedTable } from '../../components/PagedTable'
import { useOpenGeneration } from '../../hooks/use-open-generation'
import { useRemote } from '../../hooks/use-remote'
import { useRowAction, useSubmit } from '../../hooks/use-submit'
import { PERMISSIONS } from '../../menu'

const MAX_NAME_LEN = 64
const MAX_REMARK_LEN = 255
const MAX_LINK_LEN = 2048
const MAX_KEYWORD_LEN = 64
const LINK_ROWS = 3
const OWNER_PLATFORM: ChainExitOwner = 'platform'
const NO_ROUTES = 0

const OWNER_OPTIONS: { value: ChainExitOwner; label: string }[] = [
  { value: 'platform', label: '平台出口' },
  { value: 'user', label: '用户出口' },
]

const loadGroups = (signal: AbortSignal) => api.GET('/api/admin/node-groups', { signal })

/** 表单的字段：比接口输入多一层“链接可空”（修改时留空表示不改）。 */
type ExitFormValues = Omit<AdminChainExitInput, 'link'> & { link?: string }

function formOf(e: AdminChainExit | null): ExitFormValues {
  if (e === null) {
    return { name: '', link: '', remark: '', isEnabled: true, allowServer: true, allowClient: false, nodeGroupIds: [] }
  }
  return {
    name: e.name, link: '', remark: e.remark, isEnabled: e.isEnabled, allowServer: e.allowServer, allowClient: e.allowClient,
    nodeGroupIds: e.nodeGroups.map((g) => g.id),
  }
}

/**
 * 链式出口（node:read 查看，node:write 增改删、停用与查看链接；docs/03 §5.21，Ruling-50）。
 * 平台出口由管理员添加，挂到节点组后授权给用户；用户出口是用户自己添加的，这里只能停用 / 启用与删除，不能查看或修改它的链接。
 */
export function ChainExitsPage() {
  const { can } = useSession()
  const canWrite = can(PERMISSIONS.NODE_WRITE)
  const [owner, setOwner] = useState<ChainExitOwner | undefined>(undefined)
  const [userId, setUserId] = useState<number | undefined>(undefined)
  const [keyword, setKeyword] = useState('')
  const [refreshKey, setRefreshKey] = useState(0)
  const [editing, setEditing] = useState<AdminChainExit | null>(null)
  const [isOpen, setIsOpen] = useState(false)
  const groups = useRemote<NodeGroup[]>(loadGroups)
  const refresh = () => setRefreshKey((k) => k + 1)
  const act = useRowAction(refresh)

  const load = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) =>
      api.GET('/api/admin/chain-exits', { params: { query: { page, pageSize, owner, userId, keyword: keyword === '' ? undefined : keyword } }, signal }),
    [owner, userId, keyword],
  )
  const open = (e: AdminChainExit | null) => {
    setEditing(e)
    setIsOpen(true)
  }
  const setEnabled = (e: AdminChainExit, isEnabled: boolean) =>
    act(
      () => api.PUT('/api/admin/chain-exits/{id}/enabled', { params: { ...csrfHeader(), path: { id: e.id } }, body: { isEnabled } }),
      isEnabled ? '已启用' : '已停用',
    )
  const remove = (e: AdminChainExit) => act(() => api.DELETE('/api/admin/chain-exits/{id}', { params: { ...csrfHeader(), path: { id: e.id } } }), '已删除')

  const columns: TableProps<AdminChainExit>['columns'] = [
    { title: 'ID', dataIndex: 'id' },
    {
      title: '归属',
      key: 'owner',
      render: (_: unknown, e: AdminChainExit) =>
        e.user === null || e.user === undefined ? <Tag color="purple">平台出口</Tag> : <Tag color="gold">{`${e.user.username} #${e.user.id}`}</Tag>,
    },
    { title: '名称', dataIndex: 'name' },
    { title: '协议', dataIndex: 'protocol' },
    { title: '地址', key: 'address', render: (_: unknown, e: AdminChainExit) => <span className="mono">{`${e.address}:${e.port}`}</span> },
    { title: '节点组', key: 'groups', render: (_: unknown, e: AdminChainExit) => <GroupsCell exit={e} /> },
    { title: '可用于', key: 'usage', render: (_: unknown, e: AdminChainExit) => <UsageCell exit={e} /> },
    { title: '被引用', dataIndex: 'routeCount', render: (n: number) => `${n} 条规则` },
    { title: '状态', dataIndex: 'isEnabled', render: (isEnabled: boolean) => (isEnabled ? <Tag color="green">启用</Tag> : <Tag>已停用</Tag>) },
    { title: '备注', dataIndex: 'remark', render: (v: string) => v || '—' },
  ]
  if (canWrite) {
    columns.push({
      title: '操作',
      key: 'actions',
      render: (_: unknown, e: AdminChainExit) => (
        <RowActions exit={e} onEdit={() => open(e)} onToggle={() => setEnabled(e, !e.isEnabled)} onDelete={() => remove(e)} />
      ),
    })
  }

  return (
    <>
      <Space className="page-header">
        <Typography.Title level={3}>链式出口</Typography.Title>
        {canWrite && (
          <Button type="primary" icon={<PlusOutlined />} onClick={() => open(null)}>
            新建平台出口
          </Button>
        )}
      </Space>
      <Typography.Paragraph type="secondary">
        出口只用作链式规则的落地。平台出口挂到节点组后，套餐绑了该节点组的用户才能用；用户出口由用户自己添加，这里可以停用或删除，但不能查看它的链接。
      </Typography.Paragraph>
      <ErrorAlert error={groups.error} />
      <PagedTable<AdminChainExit>
        load={load}
        columns={columns}
        refreshKey={refreshKey}
        filters={
          <>
            <Select
              allowClear
              placeholder="按归属过滤"
              aria-label="按归属过滤"
              className="filter-select"
              options={OWNER_OPTIONS}
              value={owner}
              onChange={(v: ChainExitOwner | undefined) => setOwner(v)}
            />
            <InputNumber
              min={1}
              precision={0}
              placeholder="按用户 ID 过滤"
              aria-label="按用户 ID 过滤"
              className="filter-select"
              value={userId}
              onChange={(v) => setUserId(v === null ? undefined : v)}
            />
            <Input.Search
              allowClear
              maxLength={MAX_KEYWORD_LEN}
              placeholder="按名称或地址查找"
              aria-label="按名称或地址查找"
              className="filter-select"
              onSearch={(v) => setKeyword(v.trim())}
            />
          </>
        }
      />
      <ExitModal
        exit={editing}
        isOpen={isOpen}
        groups={groups.data ?? []}
        onClose={() => setIsOpen(false)}
        onSaved={() => {
          setIsOpen(false)
          refresh()
        }}
      />
    </>
  )
}

function isPlatformExit(e: AdminChainExit): boolean {
  return e.owner === OWNER_PLATFORM
}

function GroupsCell({ exit }: { exit: AdminChainExit }) {
  if (!isPlatformExit(exit)) {
    return <>—</>
  }
  if (exit.nodeGroups.length === 0) {
    return <Typography.Text type="warning">未挂节点组，没有用户能用</Typography.Text>
  }
  return (
    <>
      {exit.nodeGroups.map((g) => (
        <Tag key={g.id}>{g.name}</Tag>
      ))}
    </>
  )
}

function UsageCell({ exit }: { exit: AdminChainExit }) {
  if (!isPlatformExit(exit)) {
    return <>—</>
  }
  return (
    <>
      {exit.allowServer && <Tag color="blue">服务端</Tag>}
      {exit.allowClient && <Tag color="cyan">客户端</Tag>}
    </>
  )
}

interface RowActionsProps {
  exit: AdminChainExit
  onEdit: () => void
  onToggle: () => Promise<boolean>
  onDelete: () => Promise<boolean>
}

/** 行内操作：平台出口可编辑与查看链接；用户出口只能停用 / 启用与删除（Ruling-50）。 */
function RowActions({ exit, onEdit, onToggle, onDelete }: RowActionsProps) {
  const affected = exit.routeCount === NO_ROUTES ? '没有规则引用它。' : `会连带删除引用它的 ${exit.routeCount} 条规则。`
  return (
    <Space>
      {isPlatformExit(exit) && (
        <>
          <Button size="small" onClick={onEdit}>
            编辑
          </Button>
          <LinkButton exit={exit} />
        </>
      )}
      <Popconfirm
        title={exit.isEnabled ? `停用出口 ${exit.name}？` : `启用出口 ${exit.name}？`}
        description={exit.isEnabled ? '引用它的规则会阻断，不会被删除。' : '引用它的规则恢复生效。'}
        onConfirm={onToggle}
      >
        <Button size="small">{exit.isEnabled ? '停用' : '启用'}</Button>
      </Popconfirm>
      <Popconfirm title={`删除出口 ${exit.name}？`} description={affected} onConfirm={onDelete}>
        <Button size="small" danger>
          删除
        </Button>
      </Popconfirm>
    </Space>
  )
}

/** 查看平台出口的链接：点了才向后端取一次（记审计），只留在弹窗的内存里，关闭即丢弃。 */
function LinkButton({ exit }: { exit: AdminChainExit }) {
  const { message } = App.useApp()
  const [link, setLink] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const reveal = async () => {
    setIsBusy(true)
    try {
      const res = await api.GET('/api/admin/chain-exits/{id}/link', { params: { path: { id: exit.id } } })
      if (res.error !== undefined || res.data?.data === undefined) {
        void message.error(toApiError(res.error).message)
        return
      }
      setLink(res.data.data.link)
    } catch (err) {
      reportError(err)
      void message.error(toApiError(err).message)
    } finally {
      setIsBusy(false)
    }
  }

  return (
    <>
      <Button size="small" loading={isBusy} onClick={() => void reveal()}>
        查看链接
      </Button>
      <Modal title={`出口链接：${exit.name}`} open={link !== null} onCancel={() => setLink(null)} footer={null} destroyOnHidden>
        <Typography.Paragraph type="secondary">链接含凭据，不要外传。</Typography.Paragraph>
        <Typography.Paragraph copyable className="mono">
          {link ?? ''}
        </Typography.Paragraph>
      </Modal>
    </>
  )
}

interface ExitModalProps {
  /** null = 新建。 */
  exit: AdminChainExit | null
  isOpen: boolean
  groups: NodeGroup[]
  onClose: () => void
  onSaved: () => void
}

function ExitModal({ exit, isOpen, groups, onClose, onSaved }: ExitModalProps) {
  const generation = useOpenGeneration(isOpen)
  return (
    <Modal title={exit === null ? '新建平台出口' : `编辑平台出口：${exit.name}`} open={isOpen} onCancel={onClose} footer={null} destroyOnHidden>
      <ExitForm key={generation} exit={exit} groups={groups} onSaved={onSaved} />
    </Modal>
  )
}

function ExitForm({ exit, groups, onSaved }: Pick<ExitModalProps, 'exit' | 'groups' | 'onSaved'>) {
  const { error, isBusy, run } = useSubmit(exit === null ? '已创建平台出口' : '已保存', onSaved)
  const isNew = exit === null
  const submit = (v: ExitFormValues) => {
    const common = {
      name: v.name,
      remark: v.remark ?? '',
      isEnabled: v.isEnabled ?? true,
      allowServer: v.allowServer,
      allowClient: v.allowClient,
      nodeGroupIds: v.nodeGroupIds ?? [],
    }
    const link = (v.link ?? '').trim()
    return run(() =>
      exit === null
        ? api.POST('/api/admin/chain-exits', { params: csrfHeader(), body: { ...common, link } })
        : api.PUT('/api/admin/chain-exits/{id}', {
            params: { ...csrfHeader(), path: { id: exit.id } },
            // 修改时留空表示不改链接
            body: { ...common, link: link === '' ? undefined : link },
          }),
    )
  }
  return (
    <>
      <ErrorAlert error={error} />
      <Form<ExitFormValues> layout="vertical" initialValues={formOf(exit)} onFinish={(v) => void submit(v)}>
        <Form.Item name="name" label="名称" extra="用户在落地下拉里看到的名字" rules={[{ required: true, max: MAX_NAME_LEN, message: `1–${MAX_NAME_LEN} 个字符` }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item
          name="link"
          label="链接"
          extra={isNew ? '分享链接（vless / vmess / trojan / ss）或 socks5://[用户名:口令@]地址:端口；加密存储' : '留空表示不改链接'}
          rules={[{ required: isNew, max: MAX_LINK_LEN, message: isNew ? '请粘贴链接' : `不超过 ${MAX_LINK_LEN} 个字符` }]}
        >
          <Input.TextArea rows={LINK_ROWS} autoComplete="off" spellCheck={false} />
        </Form.Item>
        <Form.Item name="nodeGroupIds" label="节点组" extra="套餐绑了其中任一节点组的用户才能用；不选 = 没有用户能用">
          <Select mode="multiple" optionFilterProp="label" options={groups.map((g) => ({ value: g.id, label: g.name }))} />
        </Form.Item>
        <Form.Item name="allowServer" label="可用于服务端链式" valuePropName="checked" extra="链接只留在起飞面板上，用户看不到">
          <Switch />
        </Form.Item>
        <Form.Item name="allowClient" label="可用于客户端链式" valuePropName="checked" extra="链接会写进用户的订阅，用户拿得走">
          <Switch />
        </Form.Item>
        <Form.Item name="remark" label="备注" rules={[{ max: MAX_REMARK_LEN }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="isEnabled" label="启用" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          {isNew ? '创建' : '保存'}
        </Button>
      </Form>
    </>
  )
}
