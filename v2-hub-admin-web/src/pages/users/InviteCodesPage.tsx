import { PlusOutlined } from '@ant-design/icons'
import { Button, DatePicker, Form, Input, InputNumber, Popconfirm, Select, Space, Switch, Tag } from 'antd'
import dayjs, { type Dayjs } from 'dayjs'
import { useCallback, useState } from 'react'
import { api, csrfHeader, type InviteCode, type InviteCodeInput } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { ErrorAlert } from '../../components/ErrorAlert'
import { PagedTable } from '../../components/PagedTable'
import { useRowAction, useSubmit } from '../../hooks/use-submit'
import { PERMISSIONS } from '../../menu'
import { MAX_REMARK_LEN } from '../form-limits'
import { PageHeader } from '../../components/PageHeader'
import { FormModal } from '../../components/FormModal'
import { UNLIMITED_USES, inviteCodeBaseColumns } from './invite-code-columns'

const MAX_USES = 100000

type InviteKind = InviteCode['kind']

/** 邀请码类型的显示名；取值见 hub-api.yaml 的 InviteCode.kind。 */
const KIND_LABELS: Record<InviteKind, string> = { admin: '管理员码', user: '个人码', agent: '代理码' }
const KIND_COLORS: Record<InviteKind, string | undefined> = { admin: 'blue', user: undefined, agent: 'purple' }
const KIND_OPTIONS = (Object.keys(KIND_LABELS) as InviteKind[]).map((value) => ({ value, label: KIND_LABELS[value] }))

interface InviteForm {
  maxUses: number
  expiresAt?: Dayjs | null
  remark: string
  isDisabled: boolean
}

function formOf(code: InviteCode | null): InviteForm {
  if (code === null) {
    return { maxUses: 1, expiresAt: null, remark: '', isDisabled: false }
  }
  return { maxUses: code.maxUses, expiresAt: code.expiresAt === null ? null : dayjs(code.expiresAt), remark: code.remark, isDisabled: code.isDisabled }
}

/** 码的所属：个人码是用户名，代理码是代理用户名（Ruling-102），管理员码没有。 */
function ownerOf(c: InviteCode): string {
  if (c.kind === 'agent') {
    return c.agentUsername ? `代理 ${c.agentUsername}` : '代理'
  }
  return c.ownerUsername || '—'
}

function inputOf(v: InviteForm): InviteCodeInput {
  return { maxUses: v.maxUses ?? UNLIMITED_USES, expiresAt: v.expiresAt ? v.expiresAt.toISOString() : null, remark: v.remark ?? '', isDisabled: v.isDisabled }
}

/** 邀请码（user:read 查看，user:write 增改删）：管理员码与代理码可设次数与到期、可删除，个人码只能停用 / 启用（docs/03 §5.8、§5.23）。 */
export function InviteCodesPage() {
  const { can } = useSession()
  const canWrite = can(PERMISSIONS.USER_WRITE)
  const [kind, setKind] = useState<InviteKind | undefined>(undefined)
  const [editing, setEditing] = useState<InviteCode | null>(null)
  const [isOpen, setIsOpen] = useState(false)
  const [refreshKey, setRefreshKey] = useState(0)
  const reload = () => setRefreshKey((k) => k + 1)
  const loadCodes = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) =>
      api.GET('/api/admin/invite-codes', { params: { query: { page, pageSize, kind } }, signal }),
    [kind],
  )
  const act = useRowAction(reload)

  const open = (code: InviteCode | null) => {
    setEditing(code)
    setIsOpen(true)
  }

  const columns = inviteCodeBaseColumns()
  columns.splice(
    1,
    0,
    { title: '类型', dataIndex: 'kind', render: (k: InviteKind) => <Tag color={KIND_COLORS[k]}>{KIND_LABELS[k] ?? k}</Tag> },
    { title: '所属', key: 'owner', render: (_: unknown, c: InviteCode) => ownerOf(c) },
  )
  if (canWrite) {
    columns.push({
      title: '操作',
      key: 'actions',
      render: (_: unknown, c: InviteCode) => (
        <Space>
          <Button size="small" onClick={() => open(c)}>
            编辑
          </Button>
          {c.kind !== 'user' && (
            <Popconfirm
              title={`删除邀请码 ${c.code}？`}
              description="已用它注册的用户不受影响"
              onConfirm={() => act(() => api.DELETE('/api/admin/invite-codes/{id}', { params: { ...csrfHeader(), path: { id: c.id } } }), '已删除')}
            >
              <Button size="small" danger>
                删除
              </Button>
            </Popconfirm>
          )}
        </Space>
      ),
    })
  }

  return (
    <>
      <PageHeader
        title="邀请码"
        action={canWrite && (
          <Button type="primary" icon={<PlusOutlined />} onClick={() => open(null)}>
            新建邀请码
          </Button>
        )}
      />
      <PagedTable<InviteCode>
        load={loadCodes}
        columns={columns}
        refreshKey={refreshKey}
        filters={
          <Select
            allowClear
            placeholder="按类型过滤"
            aria-label="按类型过滤"
            className="filter-select"
            options={KIND_OPTIONS}
            value={kind}
            onChange={(v: InviteKind | undefined) => setKind(v)}
          />
        }
      />
      <InviteCodeModal
        code={editing}
        isOpen={isOpen}
        onClose={() => setIsOpen(false)}
        onSaved={() => {
          setIsOpen(false)
          reload()
        }}
      />
    </>
  )
}

/** 保存一个邀请码：code 为 null 时新建。 */
export type InviteCodeSave = (code: InviteCode | null, body: InviteCodeInput) => Promise<{ error?: unknown }>

/** 员工侧的保存：管理员码的新建与任意码的修改。 */
const saveAsStaff: InviteCodeSave = (code, body) =>
  code === null
    ? api.POST('/api/admin/invite-codes', { params: csrfHeader(), body })
    : api.PUT('/api/admin/invite-codes/{id}', { params: { ...csrfHeader(), path: { id: code.id } }, body })

interface InviteCodeModalProps {
  /** null = 新建（员工侧是管理员码，代理侧是代理码）。 */
  code: InviteCode | null
  isOpen: boolean
  onClose: () => void
  onSaved: () => void
  /** 保存方式；不传为员工侧接口。 */
  save?: InviteCodeSave
}

/** 邀请码的新建 / 编辑弹窗（员工与代理共用表单）。 */
export function InviteCodeModal({ code, isOpen, onClose, onSaved, save = saveAsStaff }: InviteCodeModalProps) {
  return (
    <FormModal title={code === null ? '新建邀请码' : `编辑邀请码：${code.code}`} isOpen={isOpen} onClose={onClose}>
      {(generation) => <InviteCodeForm key={generation} code={code} onSaved={onSaved} save={save} />}
    </FormModal>
  )
}

function InviteCodeForm({ code, onSaved, save }: Pick<InviteCodeModalProps, 'code' | 'onSaved'> & { save: InviteCodeSave }) {
  const { error, isBusy, run } = useSubmit(code === null ? '已创建邀请码' : '已保存', onSaved)
  // 个人码的次数取系统设置、没有到期，只能停用 / 启用
  const isPersonal = code?.kind === 'user'
  const submit = (v: InviteForm) => {
    const body = inputOf(v)
    return run(() => save(code, body))
  }
  return (
    <>
      <ErrorAlert error={error} />
      <Form<InviteForm> layout="vertical" initialValues={formOf(code)} onFinish={(v) => void submit(v)}>
        <Form.Item name="maxUses" label="可用次数" extra={isPersonal ? '个人码的次数由系统设置统一决定' : '0 表示不限；码由系统生成'}>
          <InputNumber min={UNLIMITED_USES} max={MAX_USES} precision={0} className="full-width" disabled={isPersonal} />
        </Form.Item>
        {!isPersonal && (
          <Form.Item name="expiresAt" label="到期时间" extra="留空表示不过期">
            <DatePicker showTime allowClear className="full-width" />
          </Form.Item>
        )}
        <Form.Item name="remark" label="备注" rules={[{ max: MAX_REMARK_LEN }]}>
          <Input autoComplete="off" disabled={isPersonal} />
        </Form.Item>
        <Form.Item name="isDisabled" label="停用" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          {code === null ? '创建' : '保存'}
        </Button>
      </Form>
    </>
  )
}
