import { PlusOutlined } from '@ant-design/icons'
import { App, Button, Form, Input, Modal, Popconfirm, Select, Space, Switch, Tag, Typography, type TableProps } from 'antd'
import { useCallback, useState } from 'react'
import { Link } from 'react-router'
import { api, csrfHeader, type Admin, type ApiError, type Role } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { mutate, useRemote } from '../../hooks/use-remote'
import { PERMISSIONS, ROUTES } from '../../menu'
import { MAX_ADMIN_PASSWORD_LEN, MIN_ADMIN_PASSWORD_LEN } from '../form-limits'
import { formatTime } from './format'
import { ErrorAlert } from '../../components/ErrorAlert'
import { PagedTable } from '../../components/PagedTable'
import { PageHeader } from '../../components/PageHeader'

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
  { required: true, message: '请输入密码' },
  { min: MIN_ADMIN_PASSWORD_LEN, max: MAX_ADMIN_PASSWORD_LEN, message: `密码长度须为 ${MIN_ADMIN_PASSWORD_LEN}–${MAX_ADMIN_PASSWORD_LEN} 个字符` },
]

/** 管理员管理（admin:read 查看，admin:write 增改；越权规则由后端按 docs/03 R3–R5、R11 执行，前端只做显隐）。 */
export function AdminsPage() {
  const { can, session } = useSession()
  const { message } = App.useApp()
  const canWrite = can(PERMISSIONS.ADMIN_WRITE)
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<Admin | null>(null)
  const [resetting, setResetting] = useState<Admin | null>(null)
  const [formError, setFormError] = useState<ApiError | null>(null)

  const [refreshKey, setRefreshKey] = useState(0)
  const reload = () => setRefreshKey((k) => k + 1)
  const loadAdmins = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) =>
      api.GET('/api/admin/admins', { params: { query: { page, pageSize } }, signal }),
    [],
  )
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
    reload()
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
    finish(err, '密码已重置，对方需重新登录')
  }

  const handleResetTotp = async (admin: Admin) => {
    const id = admin.id
    const err = await mutate(() => api.DELETE('/api/admin/admins/{id}/totp', { params: { ...csrfHeader(), path: { id } } }))
    if (err !== null) {
      void message.error(err.message)
      return
    }
    void message.success('已清除两步验证')
    reload()
  }

  const columns: TableProps<Admin>['columns'] = [
    {
      title: '用户名',
      dataIndex: 'username',
      render: (username: string, admin: Admin) => (admin.isOwner ? <Space>{username}<Tag color="gold">所有者</Tag></Space> : username),
    },
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
        // R4：不能经管理接口改自己，改密码去账号安全页（R9）
        if (admin.id === session?.admin.id) {
          return <Link to={ROUTES.SECURITY}>去账号安全页改密码</Link>
        }
        // R11：所有者不能经管理接口改动，按钮直接不给
        if (admin.isOwner) {
          return <Typography.Text type="secondary">所有者不可修改</Typography.Text>
        }
        return (
          <Space>
            <Button size="small" onClick={() => setEditing(admin)}>
              编辑
            </Button>
            <Button size="small" onClick={() => setResetting(admin)}>
              重置密码
            </Button>
            {admin.totpEnabled && (
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
      <PageHeader
        title="管理员"
        action={canWrite && (
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreating(true)}>
            新建管理员
          </Button>
        )}
      />
      <PagedTable<Admin>
        load={loadAdmins}
        columns={columns}
        refreshKey={refreshKey}
      />

      <Modal title="新建管理员" open={creating} onCancel={closeModals} footer={null} destroyOnHidden>
        <ErrorAlert error={formError} />
        <Form<CreateForm> layout="vertical" onFinish={handleCreate} initialValues={{ roles: [] }}>
          <Form.Item name="username" label="用户名" rules={[{ required: true, pattern: USERNAME_PATTERN, message: '3–64 位字母、数字或 _ . -' }]}>
            <Input autoComplete="off" />
          </Form.Item>
          <Form.Item name="credential" label="初始密码" rules={credentialRules}>
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item name="roles" label="角色">
            <Select mode="multiple" optionFilterProp="label" options={roleOptions} />
          </Form.Item>
          <Button type="primary" htmlType="submit" block>
            创建
          </Button>
        </Form>
      </Modal>

      <Modal title={`编辑：${editing?.username ?? ''}`} open={editing !== null} onCancel={closeModals} footer={null} destroyOnHidden>
        <ErrorAlert error={formError} />
        <Form<EditForm> layout="vertical" onFinish={handleEdit} initialValues={{ roles: editing?.roles ?? [], isDisabled: editing?.isDisabled ?? false }}>
          <Form.Item name="roles" label="角色">
            <Select mode="multiple" optionFilterProp="label" options={roleOptions} />
          </Form.Item>
          <Form.Item name="isDisabled" label="禁用账号（禁用后其会话立即失效）" valuePropName="checked">
            <Switch />
          </Form.Item>
          <Button type="primary" htmlType="submit" block>
            保存
          </Button>
        </Form>
      </Modal>

      <Modal title={`重置密码：${resetting?.username ?? ''}`} open={resetting !== null} onCancel={closeModals} footer={null} destroyOnHidden>
        <ErrorAlert error={formError} />
        <Form<ResetForm> layout="vertical" onFinish={handleReset}>
          <Form.Item name="credential" label="新密码" rules={credentialRules}>
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
