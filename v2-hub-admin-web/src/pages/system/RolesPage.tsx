import { PlusOutlined } from '@ant-design/icons'
import { App, Button, Checkbox, Form, Input, Modal, Popconfirm, Space, Table, Tag, Typography, type TableProps } from 'antd'
import { useState } from 'react'
import { api, csrfHeader, type ApiError, type Permission, type Role } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { mutate, useRemote } from '../../hooks/use-remote'
import { PERMISSIONS } from '../../menu'
import { TABLE_SCROLL } from './format'
import { ErrorAlert } from '../../components/ErrorAlert'
import { PageHeader } from '../../components/PageHeader'

const ROLE_CODE_PATTERN = /^[a-z][a-z0-9_]{2,63}$/
const MAX_ROLE_NAME_LEN = 64

const loadRoles = (signal: AbortSignal) => api.GET('/api/admin/roles', { signal })
const loadPermissions = (signal: AbortSignal) => api.GET('/api/admin/permissions', { signal })

interface RoleForm {
  code?: string
  name: string
  permissions: string[]
}

/** 角色与权限（admin:read 查看，admin:write 增删改；只能授予自己拥有的权限，见 docs/03 R2、R6）。 */
export function RolesPage() {
  const { can } = useSession()
  const { message } = App.useApp()
  const canWrite = can(PERMISSIONS.ADMIN_WRITE)
  const [editing, setEditing] = useState<Role | null>(null)
  const [isCreating, setIsCreating] = useState(false)
  const [formError, setFormError] = useState<ApiError | null>(null)

  const roles = useRemote<Role[]>(loadRoles)
  const permissions = useRemote<Permission[]>(loadPermissions)
  const permissionName = new Map((permissions.data ?? []).map((p) => [p.code, p.name]))
  // 前端只展示自己拥有的权限供勾选；后端仍按 R2 最终校验
  const grantable = (permissions.data ?? []).filter((p) => can(p.code))

  const close = () => {
    setEditing(null)
    setIsCreating(false)
    setFormError(null)
  }

  const finish = (err: ApiError | null, okText: string) => {
    if (err !== null) {
      setFormError(err)
      return
    }
    void message.success(okText)
    close()
    roles.reload()
  }

  const handleSubmit = async (values: RoleForm) => {
    const body = { name: values.name, permissions: values.permissions ?? [] }
    if (editing !== null) {
      const id = editing.id
      finish(await mutate(() => api.PUT('/api/admin/roles/{id}', { params: { ...csrfHeader(), path: { id } }, body })), '已保存')
      return
    }
    finish(await mutate(() => api.POST('/api/admin/roles', { params: csrfHeader(), body: { ...body, code: values.code } })), '已创建角色')
  }

  const handleDelete = async (role: Role) => {
    const id = role.id
    const err = await mutate(() => api.DELETE('/api/admin/roles/{id}', { params: { ...csrfHeader(), path: { id } } }))
    if (err !== null) {
      void message.error(err.message)
      return
    }
    void message.success('已删除')
    roles.reload()
  }

  const columns: TableProps<Role>['columns'] = [
    { title: '编码', dataIndex: 'code' },
    { title: '名称', dataIndex: 'name', render: (name: string, r: Role) => (r.isBuiltin ? <Space>{name}<Tag>内置</Tag></Space> : name) },
    {
      title: '权限',
      dataIndex: 'permissions',
      render: (codes: string[]) => codes.map((c) => <Tag key={c}>{permissionName.get(c) ?? c}</Tag>),
    },
  ]
  if (canWrite) {
    columns.push({
      title: '操作',
      key: 'actions',
      render: (_: unknown, role: Role) =>
        role.isBuiltin ? (
          <Typography.Text type="secondary">不可修改</Typography.Text>
        ) : (
          <Space>
            <Button size="small" onClick={() => setEditing(role)}>
              编辑
            </Button>
            <Popconfirm title={`删除角色 ${role.name}？`} description="被管理员引用的角色不能删除" onConfirm={() => handleDelete(role)}>
              <Button size="small" danger>
                删除
              </Button>
            </Popconfirm>
          </Space>
        ),
    })
  }

  const isOpen = isCreating || editing !== null
  return (
    <>
      <PageHeader
        title="角色与权限"
        action={canWrite && (
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setIsCreating(true)}>
            新建角色
          </Button>
        )}
      />
      <ErrorAlert error={roles.error} />
      <Table<Role> rowKey="id"
        scroll={TABLE_SCROLL} loading={roles.isLoading} columns={columns} dataSource={roles.data ?? []} pagination={false} />

      <Modal title={editing === null ? '新建角色' : `编辑角色：${editing.name}`} open={isOpen} onCancel={close} footer={null} destroyOnHidden>
        <ErrorAlert error={formError} />
        <Form<RoleForm>
          layout="vertical"
          onFinish={handleSubmit}
          initialValues={{ name: editing?.name ?? '', permissions: editing?.permissions ?? [] }}
        >
          {editing === null && (
            <Form.Item name="code" label="编码" rules={[{ required: true, pattern: ROLE_CODE_PATTERN, message: '小写字母开头的 3–64 位小写字母、数字或下划线' }]}>
              <Input autoComplete="off" />
            </Form.Item>
          )}
          <Form.Item name="name" label="名称" rules={[{ required: true, max: MAX_ROLE_NAME_LEN, message: `1–${MAX_ROLE_NAME_LEN} 个字符` }]}>
            <Input autoComplete="off" />
          </Form.Item>
          <Form.Item name="permissions" label="权限（只列出你拥有的权限）">
            <Checkbox.Group options={grantable.map((p) => ({ value: p.code, label: `${p.name}（${p.code}）` }))} />
          </Form.Item>
          <Button type="primary" htmlType="submit" block>
            {editing === null ? '创建' : '保存'}
          </Button>
        </Form>
      </Modal>
    </>
  )
}
