import { PlusOutlined } from '@ant-design/icons'
import { Button, Modal, Popconfirm, Space, Table, Typography, type TableProps } from 'antd'
import { useState } from 'react'
import { api, csrfHeader, type DnsAccount, type DnsProviderType } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { FormModal } from '../../components/FormModal'
import { useRemote } from '../../hooks/use-remote'
import { useRowAction } from '../../hooks/use-submit'
import { TABLE_SCROLL } from '../system/format'
import { DnsAccountForm } from './DnsAccountForm'
import { loadDnsAccounts } from './host-actions'
import { DNS_PROVIDER_LABELS, EMPTY } from './host-labels'

const MODAL_WIDTH = 880

interface DnsAccountsModalProps {
  isOpen: boolean
  /** 有 host:write 才显示新建、编辑、删除。 */
  canWrite: boolean
  onClose: () => void
  /** 账号有增删改后调用（主机列表里的账号名可能跟着变）。 */
  onChanged: () => void
}

/**
 * DNS 账号管理的弹窗（host:read 查看，host:write 增删改；docs/03 §5.22 DNS 账号，Ruling-72）。
 * 列表只有名称、服务商、根域名与被几台主机引用，没有令牌。
 */
export function DnsAccountsModal({ isOpen, canWrite, onClose, onChanged }: DnsAccountsModalProps) {
  return (
    <Modal title="DNS 账号" open={isOpen} onCancel={onClose} footer={null} destroyOnHidden width={MODAL_WIDTH}>
      <AccountList canWrite={canWrite} onChanged={onChanged} />
    </Modal>
  )
}

function AccountList({ canWrite, onChanged }: Pick<DnsAccountsModalProps, 'canWrite' | 'onChanged'>) {
  const accounts = useRemote<DnsAccount[]>(loadDnsAccounts)
  const [editing, setEditing] = useState<DnsAccount | null>(null)
  const [isCreating, setIsCreating] = useState(false)
  const refresh = () => {
    accounts.reload()
    onChanged()
  }
  const act = useRowAction(refresh)

  const closeForm = () => {
    setEditing(null)
    setIsCreating(false)
  }

  const columns: TableProps<DnsAccount>['columns'] = [
    { title: '名称', dataIndex: 'name' },
    { title: '服务商', dataIndex: 'providerType', render: (t: DnsProviderType) => DNS_PROVIDER_LABELS[t] ?? t },
    { title: '根域名', dataIndex: 'zone', render: (v: string) => <span className="mono">{v}</span> },
    { title: '引用主机', dataIndex: 'hostCount', render: (n: number) => `${n} 台` },
    { title: '备注', dataIndex: 'remark', render: (v: string) => v || EMPTY },
  ]
  if (canWrite) {
    columns.push({
      title: '操作',
      key: 'actions',
      render: (_: unknown, a: DnsAccount) => (
        <Space>
          <Button size="small" onClick={() => setEditing(a)}>
            编辑
          </Button>
          <Popconfirm
            title={`删除 DNS 账号 ${a.name}？`}
            description="仍被主机引用的账号不能删除；已经申请到的证书与解析记录不受影响，但不再自动续期"
            onConfirm={() => act(() => api.DELETE('/api/admin/dns-accounts/{id}', { params: { ...csrfHeader(), path: { id: a.id } } }), '已删除')}
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
        给主机配上 DNS 账号后，平台会把主机的域名解析过去，并向 Let’s Encrypt 申请公开可信的证书装到面板上，到期前自动续期；使用即表示同意
        Let’s Encrypt 的订阅者协议。令牌能修改这个域名的全部解析记录，加密存储、只写不读，只在平台上使用。
      </Typography.Paragraph>
      {canWrite && (
        <Button type="primary" icon={<PlusOutlined />} className="page-alert" onClick={() => setIsCreating(true)}>
          新建 DNS 账号
        </Button>
      )}
      <ErrorAlert error={accounts.error} />
      <Table<DnsAccount>
        rowKey="id"
        size="small"
        scroll={TABLE_SCROLL}
        loading={accounts.isLoading}
        columns={columns}
        dataSource={accounts.data ?? []}
        pagination={false}
      />
      <FormModal title={editing === null ? '新建 DNS 账号' : `编辑 DNS 账号：${editing.name}`} isOpen={isCreating || editing !== null} onClose={closeForm}>
        {(generation) => (
          <DnsAccountForm
            key={generation}
            account={editing}
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
