import { PlusOutlined } from '@ant-design/icons'
import { Button, Modal, Popconfirm, Space, Table, Typography, type TableProps } from 'antd'
import { useState } from 'react'
import { api, csrfHeader, type SshAuthType, type SshCredential } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { FormModal } from '../../components/FormModal'
import { useRemote } from '../../hooks/use-remote'
import { useRowAction } from '../../hooks/use-submit'
import { TABLE_SCROLL } from '../system/format'
import { loadSshCredentials } from './host-actions'
import { EMPTY, SSH_AUTH_TYPE_LABELS } from './host-labels'
import { SshCredentialForm } from './SshCredentialForm'

const MODAL_WIDTH = 880


interface SshCredentialsModalProps {
  isOpen: boolean
  /** 有 host:write 才显示新建、编辑、删除。 */
  canWrite: boolean
  onClose: () => void
  /** 凭据有增删改后调用（主机列表里的凭据名可能跟着变）。 */
  onChanged: () => void
}

/**
 * SSH 凭据管理的弹窗（host:read 查看，host:write 增删改；docs/03 §5.22 SSH 凭据）。
 * 列表只有名称、用户名、认证方式、公钥指纹与被几台主机引用，没有私钥、密码与口令。
 */
export function SshCredentialsModal({ isOpen, canWrite, onClose, onChanged }: SshCredentialsModalProps) {
  return (
    <Modal title="SSH 凭据" open={isOpen} onCancel={onClose} footer={null} destroyOnHidden width={MODAL_WIDTH}>
      <CredentialList canWrite={canWrite} onChanged={onChanged} />
    </Modal>
  )
}

function CredentialList({ canWrite, onChanged }: Pick<SshCredentialsModalProps, 'canWrite' | 'onChanged'>) {
  const credentials = useRemote<SshCredential[]>(loadSshCredentials)
  const [editing, setEditing] = useState<SshCredential | null>(null)
  const [isCreating, setIsCreating] = useState(false)
  const refresh = () => {
    credentials.reload()
    onChanged()
  }
  const act = useRowAction(refresh)

  const closeForm = () => {
    setEditing(null)
    setIsCreating(false)
  }

  const columns: TableProps<SshCredential>['columns'] = [
    { title: '名称', dataIndex: 'name' },
    { title: '用户名', dataIndex: 'username' },
    { title: '认证方式', dataIndex: 'authType', render: (t: SshAuthType) => SSH_AUTH_TYPE_LABELS[t] ?? t },
    { title: '公钥指纹', dataIndex: 'keyFingerprint', render: (v: string) => (v === '' ? EMPTY : <span className="mono">{v}</span>) },
    { title: '引用主机', dataIndex: 'hostCount', render: (n: number) => `${n} 台` },
    { title: '备注', dataIndex: 'remark', render: (v: string) => v || EMPTY },
  ]
  if (canWrite) {
    columns.push({
      title: '操作',
      key: 'actions',
      render: (_: unknown, c: SshCredential) => (
        <Space>
          <Button size="small" onClick={() => setEditing(c)}>
            编辑
          </Button>
          <Popconfirm
            title={`删除凭据 ${c.name}？`}
            description="仍被主机引用的凭据不能删除"
            onConfirm={() => act(() => api.DELETE('/api/admin/ssh-credentials/{id}', { params: { ...csrfHeader(), path: { id: c.id } } }), '已删除')}
          >
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
      <Typography.Paragraph type="secondary">
        凭据等同于这些机器的 root 权限。私钥、密码与口令加密存储、只写不读：保存后任何地方都不再显示。
      </Typography.Paragraph>
      {canWrite && (
        <Button type="primary" icon={<PlusOutlined />} className="page-alert" onClick={() => setIsCreating(true)}>
          新建凭据
        </Button>
      )}
      <ErrorAlert error={credentials.error} />
      <Table<SshCredential>
        rowKey="id"
        size="small"
        scroll={TABLE_SCROLL}
        loading={credentials.isLoading}
        columns={columns}
        dataSource={credentials.data ?? []}
        pagination={false}
      />
      <FormModal title={editing === null ? '新建 SSH 凭据' : `编辑 SSH 凭据：${editing.name}`} isOpen={isCreating || editing !== null} onClose={closeForm}>
        {(generation) => (
          <SshCredentialForm
            key={generation}
            credential={editing}
            onSaved={() => {
              closeForm()
              refresh()
            }}
          />
        )}
      </FormModal>
    </>
  )
}
