import { PlusOutlined } from '@ant-design/icons'
import { App, Button, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Switch, Table, Tag, Typography, type TableProps } from 'antd'
import { useState } from 'react'
import { api, csrfHeader, toApiError, type Plan, type SharedAccount, type SharedAccountInput } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useOpenGeneration } from '../../hooks/use-open-generation'
import { useRemote } from '../../hooks/use-remote'
import { useRowAction, useSubmit } from '../../hooks/use-submit'
import { PERMISSIONS } from '../../menu'
import { TABLE_SCROLL } from '../system/format'

const MAX_NAME_LEN = 64
const MAX_CATEGORY_LEN = 32
const MAX_TEXT_LEN = 255
const MAX_SORT_ORDER = 100000

const loadAccounts = (signal: AbortSignal) => api.GET('/api/admin/shared-accounts', { signal })
const loadPlans = (signal: AbortSignal) => api.GET('/api/admin/plans', { signal })
const NO_PLANS: Plan[] = []
// 没有 plan:read 时不请求套餐列表，“限定套餐”只显示 ID
const loadNoPlans = () => Promise.resolve({ data: { data: NO_PLANS } })

function formOf(a: SharedAccount | null): SharedAccountInput {
  if (a === null) {
    return { name: '', category: '', account: '', credential: '', remark: '', sortOrder: 0, isPublished: true, planIds: [] }
  }
  return {
    name: a.name, category: a.category, account: a.account, credential: '', remark: a.remark, sortOrder: a.sortOrder,
    isPublished: a.isPublished ?? false, planIds: a.planIds ?? [],
  }
}

/** 共享账号（content:read 查看，content:write 增改删与查看口令；docs/03 §5.9）。口令只写，查看时单独取一次并记审计。 */
export function SharedAccountsPage() {
  const { can } = useSession()
  const canWrite = can(PERMISSIONS.CONTENT_WRITE)
  const accounts = useRemote<SharedAccount[]>(loadAccounts)
  const plans = useRemote<Plan[]>(can(PERMISSIONS.PLAN_READ) ? loadPlans : loadNoPlans)
  const act = useRowAction(accounts.reload)
  const [editing, setEditing] = useState<SharedAccount | null>(null)
  const [isOpen, setIsOpen] = useState(false)

  const open = (a: SharedAccount | null) => {
    setEditing(a)
    setIsOpen(true)
  }

  return (
    <>
      <Space className="page-header">
        <Typography.Title level={3}>共享账号</Typography.Title>
        {canWrite && (
          <Button type="primary" icon={<PlusOutlined />} onClick={() => open(null)}>
            新建共享账号
          </Button>
        )}
      </Space>
      <ErrorAlert error={accounts.error} />
      <AccountTable
        accounts={accounts.data ?? []}
        isLoading={accounts.isLoading}
        plans={plans.data ?? NO_PLANS}
        canWrite={canWrite}
        onEdit={open}
        onDelete={(a) => act(() => api.DELETE('/api/admin/shared-accounts/{id}', { params: { ...csrfHeader(), path: { id: a.id } } }), '已删除')}
      />
      <AccountModal
        account={editing}
        isOpen={isOpen}
        plans={plans.data ?? NO_PLANS}
        onClose={() => setIsOpen(false)}
        onSaved={() => {
          setIsOpen(false)
          accounts.reload()
        }}
      />
    </>
  )
}

interface AccountTableProps {
  accounts: SharedAccount[]
  isLoading: boolean
  plans: Plan[]
  canWrite: boolean
  onEdit: (a: SharedAccount) => void
  onDelete: (a: SharedAccount) => Promise<boolean>
}

function AccountTable({ accounts, isLoading, plans, canWrite, onEdit, onDelete }: AccountTableProps) {
  const planName = new Map(plans.map((p) => [p.id, p.name]))
  const columns: TableProps<SharedAccount>['columns'] = [
    { title: '名称', dataIndex: 'name' },
    { title: '分类', dataIndex: 'category', render: (c: string) => <Tag color="blue">{c}</Tag> },
    { title: '账号', dataIndex: 'account', render: (v: string) => <span className="mono">{v}</span> },
    {
      title: '限定套餐',
      dataIndex: 'planIds',
      render: (ids: number[] | undefined) => ((ids ?? []).length === 0 ? '所有有套餐的用户' : (ids ?? []).map((id) => <Tag key={id}>{planName.get(id) ?? `#${id}`}</Tag>)),
    },
    { title: '排序', dataIndex: 'sortOrder' },
    { title: '状态', dataIndex: 'isPublished', render: (isPublished: boolean | undefined) => (isPublished === true ? <Tag color="green">已发布</Tag> : <Tag>未发布</Tag>) },
    { title: '备注', dataIndex: 'remark', render: (v: string) => v || '—' },
  ]
  if (canWrite) {
    columns.splice(3, 0, { title: '口令', key: 'credential', render: (_: unknown, a: SharedAccount) => <CredentialCell accountId={a.id} /> })
    columns.push({
      title: '操作',
      key: 'actions',
      render: (_: unknown, a: SharedAccount) => (
        <Space>
          <Button size="small" onClick={() => onEdit(a)}>
            编辑
          </Button>
          <Popconfirm title={`删除共享账号 ${a.name}？`} onConfirm={() => onDelete(a)}>
            <Button size="small" danger>
              删除
            </Button>
          </Popconfirm>
        </Space>
      ),
    })
  }
  return <Table<SharedAccount> rowKey="id" scroll={TABLE_SCROLL} loading={isLoading} columns={columns} dataSource={accounts} pagination={false} />
}

/** 口令默认不显示；点“显示”才向后端取一次（记审计），只留在本组件的内存里。 */
function CredentialCell({ accountId }: { accountId: number }) {
  const { message } = App.useApp()
  const [credential, setCredential] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const reveal = async () => {
    setIsBusy(true)
    try {
      const res = await api.GET('/api/admin/shared-accounts/{id}/credential', { params: { path: { id: accountId } } })
      if (res.error !== undefined || res.data?.data === undefined) {
        void message.error(toApiError(res.error).message)
        return
      }
      setCredential(res.data.data.credential)
    } catch (err) {
      reportError(err)
      void message.error(toApiError(err).message)
    } finally {
      setIsBusy(false)
    }
  }

  if (credential === null) {
    return (
      <Button size="small" loading={isBusy} onClick={() => void reveal()}>
        显示
      </Button>
    )
  }
  return (
    <Space size={4}>
      <Typography.Text copyable className="mono">
        {credential}
      </Typography.Text>
      <Button size="small" type="link" onClick={() => setCredential(null)}>
        隐藏
      </Button>
    </Space>
  )
}

interface AccountModalProps {
  /** null = 新建。 */
  account: SharedAccount | null
  isOpen: boolean
  plans: Plan[]
  onClose: () => void
  onSaved: () => void
}

function AccountModal({ account, isOpen, plans, onClose, onSaved }: AccountModalProps) {
  const generation = useOpenGeneration(isOpen)
  return (
    <Modal title={account === null ? '新建共享账号' : `编辑共享账号：${account.name}`} open={isOpen} onCancel={onClose} footer={null} destroyOnHidden>
      <AccountForm key={generation} account={account} plans={plans} onSaved={onSaved} />
    </Modal>
  )
}

function AccountForm({ account, plans, onSaved }: Pick<AccountModalProps, 'account' | 'plans' | 'onSaved'>) {
  const { error, isBusy, run } = useSubmit(account === null ? '已创建共享账号' : '已保存', onSaved)
  const isNew = account === null
  const submit = (v: SharedAccountInput) => {
    const body: SharedAccountInput = {
      name: v.name,
      category: v.category,
      account: v.account,
      // 修改时留空表示沿用原口令
      credential: v.credential || undefined,
      remark: v.remark ?? '',
      sortOrder: v.sortOrder ?? 0,
      isPublished: v.isPublished,
      planIds: v.planIds ?? [],
    }
    return run(() =>
      account === null
        ? api.POST('/api/admin/shared-accounts', { params: csrfHeader(), body })
        : api.PUT('/api/admin/shared-accounts/{id}', { params: { ...csrfHeader(), path: { id: account.id } }, body }),
    )
  }
  return (
    <>
      <ErrorAlert error={error} />
      <Form<SharedAccountInput> layout="vertical" initialValues={formOf(account)} onFinish={(v) => void submit(v)}>
        <Form.Item name="name" label="名称" rules={[{ required: true, max: MAX_NAME_LEN, message: `1–${MAX_NAME_LEN} 个字符` }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="category" label="分类" extra="如 流媒体、Apple ID" rules={[{ required: true, max: MAX_CATEGORY_LEN, message: `1–${MAX_CATEGORY_LEN} 个字符` }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="account" label="账号" rules={[{ required: true, max: MAX_TEXT_LEN, message: `1–${MAX_TEXT_LEN} 个字符` }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item
          name="credential"
          label="口令"
          extra={isNew ? '加密存储，列表里不显示' : '留空表示沿用原口令'}
          rules={[{ required: isNew, max: MAX_TEXT_LEN, message: isNew ? '请输入口令' : `不超过 ${MAX_TEXT_LEN} 个字符` }]}
        >
          <Input.Password autoComplete="new-password" />
        </Form.Item>
        <Form.Item name="planIds" label="限定套餐" extra="不选 = 所有有套餐的用户可见">
          <Select mode="multiple" optionFilterProp="label" options={plans.map((p) => ({ value: p.id, label: p.name }))} />
        </Form.Item>
        <Form.Item name="remark" label="备注" extra="用户可见，可写使用须知" rules={[{ max: MAX_TEXT_LEN }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="sortOrder" label="排序" extra="小的在前">
          <InputNumber min={0} max={MAX_SORT_ORDER} precision={0} className="full-width" />
        </Form.Item>
        <Form.Item name="isPublished" label="发布" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          {isNew ? '创建' : '保存'}
        </Button>
      </Form>
    </>
  )
}
