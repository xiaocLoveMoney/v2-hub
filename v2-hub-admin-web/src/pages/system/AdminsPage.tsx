import { PlusOutlined } from '@ant-design/icons'
import { Alert, App, Button, Form, Input, Modal, Popconfirm, Select, Space, Switch, Table, Tag, Typography, type TableProps } from 'antd'
import { useCallback, useState } from 'react'
import { api, csrfHeader, type Admin, type ApiError, type PageData, type Role } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { mutate, useRemote } from '../../hooks/use-remote'
import { PERMISSIONS } from '../../menu'
import { TABLE_SCROLL, formatTime } from './format'

const DEFAULT_PAGE_SIZE = 20
const MIN_CREDENTIAL_LEN = 12
const MAX_CREDENTIAL_LEN = 128
const USERNAME_PATTERN = /^[a-zA-Z0-9_.-]{3,64}$/

interface CreateForm {
  username: string
  credential: string
  roles: string[]
}

interface EditForm {
  roles: string[]
  isDisabled: boolean
}

interface ResetForm {
  credential: string
}

/** 角色列表请求与组件状态无关，提到模块级保持引用稳定。 */
const loadRoles = (signal: AbortSignal) => api.GET('/api/admin/roles', { signal })

const credentialRules = [
  { required: true, message: '请输入口令' },
  { min: MIN_CREDENTIAL_LEN, max: MAX_CREDENTIAL_LEN, message: `口令长度须为 ${MIN_CREDENTIAL_LEN}–${MAX_CREDENTIAL_LEN} 个字符` },
]

/** 管理员管理（admin:read 查看，admin:write 增改；越权规则由后端按 docs/03 R2–R5 执行）。 */
export function AdminsPage() {
  const { can, session } = useSession()
  const { message } = App.useApp()
  const canWrite = can(PERMISSIONS.ADMIN_WRITE)
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(DEFAULT_PAGE_SIZE)
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<Admin | null>(null)
  const [resetting, setResetting] = useState<Admin | null>(null)
  const [formError, setFormError] = useState<ApiError | null>(null)

  const loadAdmins = useCallback(
    (signal: AbortSignal) => api.GET('/api/admin/admins', { params: { query: { page, pageSize } }, signal }),
    [page, pageSize],
  )
  const admins = useRemote<PageData<Admin>>(loadAdmins)
  const roles = useRemote<Role[]>(loadRoles)
  const roleOptions = (roles.data ?? []).map((r) => ({ value: r.code, label: `${r.name}（${r.code}）` }))

  const closeModals = () => {
    setCreating(false)
    setEditing(null)
    setResetting(null)
    setFormError(null)
  }

  const finish = (err: ApiError | null, okText: string) => {
    if (err !== null) {
      setFormError(err)
      return
    }
    void message.success(okText)
    closeModals()
    admins.reload()
  }

  const handleCreate = async (values: CreateForm) => {
    const pw = values.credential
    const err = await mutate(() =>
      api.POST('/api/admin/admins', { params: csrfHeader(), body: { username: values.username, password: pw, roles: values.roles ?? [] } }),
    )
    finish(err, '已创建管理员')
  }

  const handleEdit = async (values: EditForm) => {
    if (editing === null) {
      return
    }
    const id = editing.id
    const err = await mutate(() =>
      api.PUT('/api/admin/admins/{id}', { params: { ...csrfHeader(), path: { id } }, body: { roles: values.roles ?? [], isDisabled: values.isDisabled } }),
    )
    finish(err, '已保存')
  }

  const handleReset = async (values: ResetForm) => {
    if (resetting === null) {
      return
    }
    const id = resetting.id
    const pw = values.credential
    const err = await mutate(() =>
      api.PUT('/api/admin/admins/{id}/password', { params: { ...csrfHeader(), path: { id } }, body: { password: pw } }),
    )
    finish(err, '口令已重置，对方需重新登录')
  }

  const handleResetTotp = async (admin: Admin) => {
    const id = admin.id
    const err = await mutate(() => api.DELETE('/api/admin/admins/{id}/totp', { params: { ...csrfHeader(), path: { id } } }))
    if (err !== null) {
      void message.error(err.message)
      return
    }
    void message.success('已清除两步验证')
    admins.reload()
  }

  const columns: TableProps<Admin>['columns'] = [
    { title: '用户名', dataIndex: 'username' },
    {
      title: '角色',
      dataIndex: 'roles',
      render: (codes: string[]) => (codes.length === 0 ? <Typography.Text type="secondary">无</Typography.Text> : codes.map((c) => <Tag key={c}>{c}</Tag>)),
    },
    { title: '状态', dataIndex: 'isDisabled', render: (isDisabled: boolean) => (isDisabled ? <Tag color="red">已禁用</Tag> : <Tag color="green">启用</Tag>) },
    { title: '两步验证', dataIndex: 'totpEnabled', render: (on: boolean) => (on ? <Tag color="blue">已开启</Tag> : <Tag>未开启</Tag>) },
    { title: '最近登录', dataIndex: 'lastLoginAt', render: (v: string | null | undefined) => formatTime(v) },
    { title: '创建时间', dataIndex: 'createdAt', render: (v: string) => formatTime(v) },
  ]
  if (canWrite) {
    columns.push({
      title: '操作',
      key: 'actions',
      render: (_: unknown, admin: Admin) => {
        // R4：不能经管理接口改自己，按钮直接不给
        const isSelf = admin.id === session?.admin.id
        return (
          <Space>
            <Button size="small" disabled={isSelf} onClick={() => setEditing(admin)}>
              编辑
            </Button>
            <Button size="small" onClick={() => setResetting(admin)}>
              重置口令
            </Button>
            {admin.totpEnabled && !isSelf && (
              <Popconfirm
                title={`清除 ${admin.username} 的两步验证？`}
                description="用于对方遗失验证器设备；清除后其全部会话失效"
                onConfirm={() => handleResetTotp(admin)}
              >
                <Button size="small">清除两步验证</Button>
              </Popconfirm>
            )}
          </Space>
        )
      },
    })
  }

  return (
    <>
      <Space className="page-header">
        <Typography.Title level={3}>管理员</Typography.Title>
        {canWrite && (
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreating(true)}>
            新建管理员
          </Button>
        )}
      </Space>
      {admins.error !== null && <Alert type="error" title={admins.error.message} showIcon className="page-alert" />}
      <Table<Admin>
        rowKey="id"
        scroll={TABLE_SCROLL}
        loading={admins.isLoading}
        columns={columns}
        dataSource={admins.data?.items ?? []}
        pagination={{
          current: page,
          pageSize,
          total: admins.data?.total ?? 0,
          onChange: (p, size) => {
            setPage(p)
            setPageSize(size)
          },
        }}
      />

      <Modal title="新建管理员" open={creating} onCancel={closeModals} footer={null} destroyOnHidden>
        {formError !== null && <Alert type="error" title={formError.message} showIcon className="page-alert" />}
        <Form<CreateForm> layout="vertical" onFinish={handleCreate} initialValues={{ roles: [] }}>
          <Form.Item name="username" label="用户名" rules={[{ required: true, pattern: USERNAME_PATTERN, message: '3–64 位字母、数字或 _ . -' }]}>
            <Input autoComplete="off" />
          </Form.Item>
          <Form.Item name="credential" label="初始口令" rules={credentialRules}>
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item name="roles" label="角色">
            <Select mode="multiple" options={roleOptions} />
          </Form.Item>
          <Button type="primary" htmlType="submit" block>
            创建
          </Button>
        </Form>
      </Modal>

      <Modal title={`编辑：${editing?.username ?? ''}`} open={editing !== null} onCancel={closeModals} footer={null} destroyOnHidden>
        {formError !== null && <Alert type="error" title={formError.message} showIcon className="page-alert" />}
        <Form<EditForm> layout="vertical" onFinish={handleEdit} initialValues={{ roles: editing?.roles ?? [], isDisabled: editing?.isDisabled ?? false }}>
          <Form.Item name="roles" label="角色">
            <Select mode="multiple" options={roleOptions} />
          </Form.Item>
          <Form.Item name="isDisabled" label="禁用账号（禁用后其会话立即失效）" valuePropName="checked">
            <Switch />
          </Form.Item>
          <Button type="primary" htmlType="submit" block>
            保存
          </Button>
        </Form>
      </Modal>

      <Modal title={`重置口令：${resetting?.username ?? ''}`} open={resetting !== null} onCancel={closeModals} footer={null} destroyOnHidden>
        {formError !== null && <Alert type="error" title={formError.message} showIcon className="page-alert" />}
        <Form<ResetForm> layout="vertical" onFinish={handleReset}>
          <Form.Item name="credential" label="新口令" rules={credentialRules}>
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Button type="primary" htmlType="submit" block>
            重置
          </Button>
        </Form>
      </Modal>
    </>
  )
}
