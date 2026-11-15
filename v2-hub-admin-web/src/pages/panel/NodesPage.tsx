import { PlusOutlined } from '@ant-design/icons'
import { App, Button, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Switch, Table, Tabs, Tag, Typography, type TableProps } from 'antd'
import { useCallback, useState } from 'react'
import { api, csrfHeader, type ApiError, type NodeGroup, type PageData, type PanelNode, type Server } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { mutate, useRemote } from '../../hooks/use-remote'
import { PERMISSIONS } from '../../menu'
import { TABLE_SCROLL } from '../system/format'
import { formatCountry } from './countries'
import { ErrorAlert } from '../../components/ErrorAlert'

const DEFAULT_PAGE_SIZE = 20
/** 节点组成员选择器一次拉取的节点数上限（与接口 pageSize 上限一致）。 */
const PICKER_PAGE_SIZE = 100

const loadServers = (signal: AbortSignal) => api.GET('/api/admin/servers', { signal })
const loadGroups = (signal: AbortSignal) => api.GET('/api/admin/node-groups', { signal })
const loadPickerNodes = (signal: AbortSignal) => api.GET('/api/admin/nodes', { params: { query: { pageSize: PICKER_PAGE_SIZE } }, signal })

interface NodeForm {
  displayName: string
  tags?: string[]
  sortOrder?: number
  isEnabled: boolean
}

interface GroupForm {
  name: string
  remark?: string
  nodeIds?: number[]
}

/** 节点目录与节点组（node:read 查看，node:write 修改；展示属性只影响平台，不写回面板）。 */
export function NodesPage() {
  const { can } = useSession()
  const canWrite = can(PERMISSIONS.NODE_WRITE)
  const servers = useRemote<Server[]>(loadServers)
  const groups = useRemote<NodeGroup[]>(loadGroups)
  return (
    <>
      <Typography.Title level={3}>节点</Typography.Title>
      <Tabs
        items={[
          { key: 'nodes', label: '节点目录', children: <NodeCatalog canWrite={canWrite} servers={servers.data ?? []} groups={groups.data ?? []} /> },
          { key: 'groups', label: '节点组', children: <GroupList canWrite={canWrite} groups={groups.data ?? []} error={groups.error} onChanged={groups.reload} /> },
        ]}
      />
    </>
  )
}

interface NodeCatalogProps {
  canWrite: boolean
  servers: Server[]
  groups: NodeGroup[]
}

function NodeCatalog({ canWrite, servers, groups }: NodeCatalogProps) {
  const { message } = App.useApp()
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(DEFAULT_PAGE_SIZE)
  const [serverId, setServerId] = useState<number | undefined>(undefined)
  const [groupId, setGroupId] = useState<number | undefined>(undefined)
  const [editing, setEditing] = useState<PanelNode | null>(null)
  const [error, setError] = useState<ApiError | null>(null)

  const load = useCallback(
    (signal: AbortSignal) => api.GET('/api/admin/nodes', { params: { query: { page, pageSize, serverId, groupId } }, signal }),
    [page, pageSize, serverId, groupId],
  )
  const nodes = useRemote<PageData<PanelNode>>(load)
  const groupName = new Map(groups.map((g) => [g.id, g.name]))

  const handleSave = async (v: NodeForm) => {
    if (editing === null) {
      return
    }
    const id = editing.id
    const err = await mutate(() =>
      api.PUT('/api/admin/nodes/{id}', {
        params: { ...csrfHeader(), path: { id } },
        body: { displayName: v.displayName, tags: v.tags ?? [], sortOrder: v.sortOrder ?? 0, isEnabled: v.isEnabled },
      }),
    )
    if (err !== null) {
      setError(err)
      return
    }
    void message.success('已保存')
    setEditing(null)
    nodes.reload()
  }

  const columns: TableProps<PanelNode>['columns'] = [
    {
      title: '显示名',
      dataIndex: 'displayName',
      render: (name: string, n: PanelNode) => (
        <Space>
          {name}
          {n.isMissing && <Tag color="red">来源已不存在</Tag>}
          {!n.isEnabled && <Tag>已停用</Tag>}
        </Space>
      ),
    },
    { title: '服务器', dataIndex: 'serverName' },
    { title: '协议', dataIndex: 'protocol' },
    { title: '地址', key: 'address', render: (_: unknown, n: PanelNode) => `${n.address}:${n.port}${n.endpointId === null || n.endpointId === undefined ? '' : '（Host）'}` },
    { title: '地区', dataIndex: 'region', render: (v: string) => formatCountry(v) },
    { title: '标签', dataIndex: 'tags', render: (tags: string[]) => tags.map((t) => <Tag key={t}>{t}</Tag>) },
    { title: '节点组', dataIndex: 'groupIds', render: (ids: number[]) => ids.map((id) => <Tag key={id} color="blue">{groupName.get(id) ?? id}</Tag>) },
    { title: '排序', dataIndex: 'sortOrder' },
  ]
  if (canWrite) {
    columns.push({
      title: '操作',
      key: 'actions',
      render: (_: unknown, n: PanelNode) => (
        <Button size="small" onClick={() => setEditing(n)}>
          编辑
        </Button>
      ),
    })
  }

  return (
    <>
      <Space className="page-filters">
        <Select
          allowClear
          placeholder="按服务器过滤"
          className="filter-select"
          options={servers.map((s) => ({ value: s.id, label: s.name }))}
          value={serverId}
          onChange={(v: number | undefined) => {
            setServerId(v)
            setPage(1)
          }}
        />
        <Select
          allowClear
          placeholder="按节点组过滤"
          className="filter-select"
          options={groups.map((g) => ({ value: g.id, label: g.name }))}
          value={groupId}
          onChange={(v: number | undefined) => {
            setGroupId(v)
            setPage(1)
          }}
        />
      </Space>
      <ErrorAlert error={nodes.error} />
      <Table<PanelNode>
        rowKey="id"
        scroll={TABLE_SCROLL}
        loading={nodes.isLoading}
        columns={columns}
        dataSource={nodes.data?.items ?? []}
        pagination={{
          current: page,
          pageSize,
          total: nodes.data?.total ?? 0,
          onChange: (p, size) => {
            setPage(p)
            setPageSize(size)
          },
        }}
      />
      <Modal
        title={`编辑节点：${editing?.displayName ?? ''}`}
        open={editing !== null}
        onCancel={() => {
          setEditing(null)
          setError(null)
        }}
        footer={null}
        destroyOnHidden
      >
        <ErrorAlert error={error} />
        <Form<NodeForm>
          layout="vertical"
          onFinish={handleSave}
          initialValues={{
            displayName: editing?.displayName ?? '',
            tags: editing?.tags ?? [],
            sortOrder: editing?.sortOrder ?? 0,
            isEnabled: editing?.isEnabled ?? true,
          }}
        >
          <Form.Item name="displayName" label="显示名" rules={[{ required: true, max: 128, message: '1–128 个字符' }]}>
            <Input autoComplete="off" />
          </Form.Item>
          <Form.Item label="地区" extra="跟随所在服务器的位置，在服务器里修改">
            <Input aria-label="地区" value={formatCountry(editing?.region, '未知')} disabled />
          </Form.Item>
          <Form.Item name="tags" label="标签" extra="回车添加">
            <Select mode="tags" open={false} />
          </Form.Item>
          <Form.Item name="sortOrder" label="排序（小的在前）">
            <InputNumber className="full-width" />
          </Form.Item>
          <Form.Item name="isEnabled" label="启用" valuePropName="checked">
            <Switch />
          </Form.Item>
          <Button type="primary" htmlType="submit" block>
            保存
          </Button>
        </Form>
      </Modal>
    </>
  )
}

interface GroupListProps {
  canWrite: boolean
  groups: NodeGroup[]
  error: ApiError | null
  onChanged: () => void
}

function GroupList({ canWrite, groups, error, onChanged }: GroupListProps) {
  const { message } = App.useApp()
  const [editing, setEditing] = useState<NodeGroup | null>(null)
  const [isCreating, setIsCreating] = useState(false)
  const [formError, setFormError] = useState<ApiError | null>(null)
  const picker = useRemote<PageData<PanelNode>>(loadPickerNodes)
  const nodeOptions = (picker.data?.items ?? []).map((n) => ({ value: n.id, label: `${n.displayName}（${n.serverName}）` }))

  const close = () => {
    setEditing(null)
    setIsCreating(false)
    setFormError(null)
  }

  const handleSave = async (v: GroupForm) => {
    const body = { name: v.name, remark: v.remark ?? '', nodeIds: v.nodeIds ?? [] }
    const err =
      editing === null
        ? await mutate(() => api.POST('/api/admin/node-groups', { params: csrfHeader(), body }))
        : await mutate(() => api.PUT('/api/admin/node-groups/{id}', { params: { ...csrfHeader(), path: { id: editing.id } }, body }))
    if (err !== null) {
      setFormError(err)
      return
    }
    void message.success('已保存')
    close()
    onChanged()
  }

  const handleDelete = async (g: NodeGroup) => {
    const err = await mutate(() => api.DELETE('/api/admin/node-groups/{id}', { params: { ...csrfHeader(), path: { id: g.id } } }))
    if (err !== null) {
      void message.error(err.message)
      return
    }
    void message.success('已删除')
    onChanged()
  }

  const columns: TableProps<NodeGroup>['columns'] = [
    { title: '名称', dataIndex: 'name' },
    { title: '备注', dataIndex: 'remark' },
    { title: '节点数', dataIndex: 'nodeIds', render: (ids: number[]) => ids.length },
  ]
  if (canWrite) {
    columns.push({
      title: '操作',
      key: 'actions',
      render: (_: unknown, g: NodeGroup) => (
        <Space>
          <Button size="small" onClick={() => setEditing(g)}>
            编辑
          </Button>
          <Popconfirm title={`删除节点组 ${g.name}？`} description="不会删除节点" onConfirm={() => handleDelete(g)}>
            <Button size="small" danger>
              删除
            </Button>
          </Popconfirm>
        </Space>
      ),
    })
  }

  return (
    <>
      {canWrite && (
        <Space className="page-filters">
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setIsCreating(true)}>
            新建节点组
          </Button>
        </Space>
      )}
      <ErrorAlert error={error} />
      <Table<NodeGroup> rowKey="id" scroll={TABLE_SCROLL} columns={columns} dataSource={groups} pagination={false} />
      <Modal title={editing === null ? '新建节点组' : `编辑节点组：${editing.name}`} open={isCreating || editing !== null} onCancel={close} footer={null} destroyOnHidden>
        <ErrorAlert error={formError} />
        <Form<GroupForm> layout="vertical" onFinish={handleSave} initialValues={{ name: editing?.name ?? '', remark: editing?.remark ?? '', nodeIds: editing?.nodeIds ?? [] }}>
          <Form.Item name="name" label="名称" rules={[{ required: true, max: 64, message: '1–64 个字符' }]}>
            <Input autoComplete="off" />
          </Form.Item>
          <Form.Item name="remark" label="备注">
            <Input autoComplete="off" />
          </Form.Item>
          <Form.Item name="nodeIds" label="节点">
            <Select mode="multiple" options={nodeOptions} optionFilterProp="label" loading={picker.isLoading} />
          </Form.Item>
          <Button type="primary" htmlType="submit" block>
            保存
          </Button>
        </Form>
      </Modal>
    </>
  )
}
