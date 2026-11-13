import { Alert, App, Button, DatePicker, Descriptions, Drawer, Form, Input, InputNumber, Modal, Select, Space, Switch, Table, Tag, Typography } from 'antd'
import dayjs, { type Dayjs } from 'dayjs'
import { useCallback, useState } from 'react'
import { api, csrfHeader, type EndUser, type Plan, type Provision } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useOpenGeneration } from '../../hooks/use-open-generation'
import { mutate, useRemote } from '../../hooks/use-remote'
import { useSubmit } from '../../hooks/use-submit'
import { TABLE_SCROLL, formatTime } from '../system/format'
import { RenewResetFields, type RenewResetForm } from './quota-fields'
import { bytesToGb, describeRenew, describeReset, formatBytes, gbToBytes, renewFieldsOf, renewModeOf, renewValueOf } from './quota'

// 终端用户口令 8–128 位（docs/03 §5.3；管理员口令是 12 位起）
const MIN_CREDENTIAL_LEN = 8
const MAX_CREDENTIAL_LEN = 128
const MAX_REMARK_LEN = 255
const MAX_LIMIT_IP = 1000
const MAX_PENDING_DAYS = 36500
// 新用户名不含 @，好与邮箱登录区分（docs/03 §5.8）
const USERNAME_PATTERN = /^[A-Za-z0-9_.-]{3,64}$/
const MAX_EMAIL_LEN = 254
const emailRules = [{ type: 'email' as const, max: MAX_EMAIL_LEN, message: '邮箱格式不对' }]

const credentialRules = [
  { required: true, message: '请输入口令' },
  { min: MIN_CREDENTIAL_LEN, max: MAX_CREDENTIAL_LEN, message: `口令长度须为 ${MIN_CREDENTIAL_LEN}–${MAX_CREDENTIAL_LEN} 个字符` },
]

// ---- 新建 -------------------------------------------------------------------------

interface CreateForm {
  username: string
  credential: string
  email?: string
  remark: string
  planId?: number
}

interface CreateUserModalProps {
  isOpen: boolean
  plans: Plan[]
  onClose: () => void
  onSaved: () => void
}

/** 新建用户，可同时分配套餐（docs/03 U1、U6）。 */
export function CreateUserModal({ isOpen, plans, onClose, onSaved }: CreateUserModalProps) {
  const generation = useOpenGeneration(isOpen)
  return (
    <Modal title="新建用户" open={isOpen} onCancel={onClose} footer={null} destroyOnHidden>
      <CreateUserForm key={generation} plans={plans} onSaved={onSaved} />
    </Modal>
  )
}

function CreateUserForm({ plans, onSaved }: { plans: Plan[]; onSaved: () => void }) {
  const { error, isBusy, run } = useSubmit('已创建用户，正在下发到各面板', onSaved)
  const submit = (v: CreateForm) => {
    const pw = v.credential
    return run(() => api.POST('/api/admin/users', { params: csrfHeader(), body: { username: v.username, password: pw, email: v.email || undefined, remark: v.remark ?? '', planId: v.planId } }))
  }
  return (
    <>
      <ErrorAlert error={error} />
      <Form<CreateForm> layout="vertical" onFinish={(v) => void submit(v)}>
        <Form.Item name="username" label="用户名" extra="登录客户后台用" rules={[{ required: true, pattern: USERNAME_PATTERN, message: '3–64 位字母、数字或 _ . -' }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="email" label="邮箱" extra="可选；填了即视为已验证，可用来登录与找回口令" rules={emailRules}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="credential" label="初始口令" rules={credentialRules}>
          <Input.Password autoComplete="new-password" />
        </Form.Item>
        <Form.Item name="planId" label="套餐" extra="不选则暂无可用节点，之后可在“续费”里分配">
          <Select allowClear options={plans.map((p) => ({ value: p.id, label: p.name }))} />
        </Form.Item>
        <Form.Item name="remark" label="备注" rules={[{ max: MAX_REMARK_LEN }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          创建
        </Button>
      </Form>
    </>
  )
}

// ---- 编辑（备注、停用） ----------------------------------------------------------------

interface UserModalProps {
  user: EndUser | null
  onClose: () => void
  onSaved: () => void
}

/** 修改备注与停用 / 启用（docs/03 U8）。 */
export function EditUserModal({ user, onClose, onSaved }: UserModalProps) {
  const generation = useOpenGeneration(user !== null)
  return (
    <Modal title={`编辑用户：${user?.username ?? ''}`} open={user !== null} onCancel={onClose} footer={null} destroyOnHidden>
      {user !== null && <EditUserForm key={generation} user={user} onSaved={onSaved} />}
    </Modal>
  )
}

function EditUserForm({ user, onSaved }: { user: EndUser; onSaved: () => void }) {
  const { error, isBusy, run } = useSubmit('已保存', onSaved)
  const currentEmail = user.email ?? ''
  const submit = (v: { remark: string; isDisabled: boolean; email?: string }) => {
    // 邮箱没改就不带，免得把“未验证”的邮箱顺手标成已验证
    const email = (v.email ?? '') === currentEmail ? undefined : (v.email ?? '')
    return run(() =>
      api.PUT('/api/admin/users/{id}', { params: { ...csrfHeader(), path: { id: user.id } }, body: { remark: v.remark ?? '', isDisabled: v.isDisabled, email } }),
    )
  }
  return (
    <>
      <ErrorAlert error={error} />
      <Form layout="vertical" initialValues={{ remark: user.remark, isDisabled: user.isDisabled, email: currentEmail }} onFinish={(v) => void submit(v)}>
        <Form.Item name="email" label="邮箱" extra="清空即解绑；改成新地址即视为已验证" rules={emailRules}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="remark" label="备注" rules={[{ max: MAX_REMARK_LEN }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="isDisabled" label="停用（各面板上的 client 随之禁用）" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          保存
        </Button>
      </Form>
    </>
  )
}

// ---- 续费 / 分配套餐 ----------------------------------------------------------------

/** 分配或续费套餐：未到期从原到期日顺延，已到期从现在起算，已用清零（docs/03 U6）。 */
export function AssignPlanModal({ user, plans, onClose, onSaved }: UserModalProps & { plans: Plan[] }) {
  const generation = useOpenGeneration(user !== null)
  return (
    <Modal title={`续费：${user?.username ?? ''}`} open={user !== null} onCancel={onClose} footer={null} destroyOnHidden>
      {user !== null && <AssignPlanForm key={generation} user={user} plans={plans} onSaved={onSaved} />}
    </Modal>
  )
}

function AssignPlanForm({ user, plans, onSaved }: { user: EndUser; plans: Plan[]; onSaved: () => void }) {
  const { error, isBusy, run } = useSubmit('已续费', onSaved)
  const submit = (v: { planId: number }) =>
    run(() => api.POST('/api/admin/users/{id}/plan', { params: { ...csrfHeader(), path: { id: user.id } }, body: { planId: v.planId } }))
  return (
    <>
      <ErrorAlert error={error} />
      <Descriptions size="small" column={1} className="page-alert">
        <Descriptions.Item label="当前套餐">{user.planName || '—'}</Descriptions.Item>
        <Descriptions.Item label="当前到期">{user.expiresAt === null || user.expiresAt === undefined ? '不过期 / 未开始' : formatTime(user.expiresAt)}</Descriptions.Item>
        <Descriptions.Item label="已用">{formatBytes(user.trafficUsed)}</Descriptions.Item>
      </Descriptions>
      <Form layout="vertical" initialValues={{ planId: user.planId ?? undefined }} onFinish={(v) => void submit(v)}>
        <Form.Item name="planId" label="套餐" extra="未到期从原到期日顺延，已到期从现在起算；已用流量清零" rules={[{ required: true, message: '请选择套餐' }]}>
          <Select options={plans.map((p) => ({ value: p.id, label: p.name }))} />
        </Form.Item>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          续费
        </Button>
      </Form>
    </>
  )
}

// ---- 改用量 -------------------------------------------------------------------------

interface QuotaForm extends RenewResetForm {
  trafficLimitGb: number
  trafficUsedGb: number
  expiresAt: Dayjs | null
  pendingDays: number
  limitIp: number
}

function quotaFormOf(u: EndUser): QuotaForm {
  const renew = { renewEveryDays: u.renewEveryDays, renewMonthDay: u.renewMonthDay, renewWeekday: u.renewWeekday }
  return {
    trafficLimitGb: bytesToGb(u.trafficLimit), trafficUsedGb: bytesToGb(u.trafficUsed),
    expiresAt: u.expiresAt === null || u.expiresAt === undefined ? null : dayjs(u.expiresAt), pendingDays: u.pendingDays, limitIp: u.limitIp,
    renewMode: renewModeOf(renew), renewValue: renewValueOf(renew), renewMax: u.renewMax, trafficReset: u.trafficReset, trafficResetDay: u.trafficResetDay,
  }
}

/** 管理员直接改已用、额度、到期、IP 限制、自动续期与周期重置（docs/03 U7，记审计）。 */
export function QuotaModal({ user, onClose, onSaved }: UserModalProps) {
  const generation = useOpenGeneration(user !== null)
  return (
    <Modal title={`改用量：${user?.username ?? ''}`} open={user !== null} onCancel={onClose} footer={null} destroyOnHidden width={600}>
      {user !== null && <QuotaForm key={generation} user={user} onSaved={onSaved} />}
    </Modal>
  )
}

function QuotaForm({ user, onSaved }: { user: EndUser; onSaved: () => void }) {
  const [form] = Form.useForm<QuotaForm>()
  const expiresAt = Form.useWatch('expiresAt', form)
  const { error, isBusy, run } = useSubmit('已保存，正在同步到各面板', onSaved)
  const submit = (v: QuotaForm) =>
    run(() =>
      api.PUT('/api/admin/users/{id}/quota', {
        params: { ...csrfHeader(), path: { id: user.id } },
        body: {
          trafficLimit: gbToBytes(v.trafficLimitGb), trafficUsed: gbToBytes(v.trafficUsedGb),
          expiresAt: v.expiresAt === null || v.expiresAt === undefined ? null : v.expiresAt.toISOString(),
          pendingDays: v.expiresAt === null || v.expiresAt === undefined ? (v.pendingDays ?? 0) : 0, limitIp: v.limitIp ?? 0,
          ...renewFieldsOf(v.renewMode, v.renewValue), renewMax: v.renewMode === 'none' ? 0 : (v.renewMax ?? 0),
          trafficReset: v.trafficReset, trafficResetDay: v.trafficResetDay ?? 1,
        },
      }),
    )
  return (
    <>
      <ErrorAlert error={error} />
      <Form<QuotaForm> form={form} layout="vertical" initialValues={quotaFormOf(user)} onFinish={(v) => void submit(v)}>
        <Form.Item label="已用流量（GB）" extra={`当前 ${formatBytes(user.trafficUsed)}`}>
          <Space.Compact className="full-width">
            <Form.Item name="trafficUsedGb" noStyle>
              <InputNumber min={0} className="full-width" aria-label="已用流量（GB）" />
            </Form.Item>
            <Button onClick={() => form.setFieldValue('trafficUsedGb', 0)}>清零</Button>
          </Space.Compact>
        </Form.Item>
        <Form.Item name="trafficLimitGb" label="流量额度（GB）" extra="0 表示不限">
          <InputNumber min={0} className="full-width" />
        </Form.Item>
        <Form.Item name="expiresAt" label="到期时间" extra="清空表示不过期，或配合下方天数“首次使用后生效”">
          <DatePicker showTime allowClear className="full-width" />
        </Form.Item>
        {(expiresAt === null || expiresAt === undefined) && (
          <Form.Item name="pendingDays" label="首次使用后生效的天数" extra="0 表示不过期">
            <InputNumber min={0} max={MAX_PENDING_DAYS} className="full-width" />
          </Form.Item>
        )}
        <Form.Item name="limitIp" label="同时在线 IP 数" extra="0 表示不限">
          <InputNumber min={0} max={MAX_LIMIT_IP} className="full-width" />
        </Form.Item>
        <RenewResetFields />
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          保存
        </Button>
      </Form>
    </>
  )
}

// ---- 重置口令 -----------------------------------------------------------------------

/** 重置客户后台登录口令。 */
export function ResetUserCredentialModal({ user, onClose, onSaved }: UserModalProps) {
  const generation = useOpenGeneration(user !== null)
  return (
    <Modal title={`重置口令：${user?.username ?? ''}`} open={user !== null} onCancel={onClose} footer={null} destroyOnHidden>
      {user !== null && <ResetCredentialForm key={generation} user={user} onSaved={onSaved} />}
    </Modal>
  )
}

function ResetCredentialForm({ user, onSaved }: { user: EndUser; onSaved: () => void }) {
  const { error, isBusy, run } = useSubmit('口令已重置', onSaved)
  const submit = (v: { credential: string }) => {
    const pw = v.credential
    return run(() => api.PUT('/api/admin/users/{id}/password', { params: { ...csrfHeader(), path: { id: user.id } }, body: { password: pw } }))
  }
  return (
    <>
      <ErrorAlert error={error} />
      <Form layout="vertical" onFinish={(v) => void submit(v)}>
        <Form.Item name="credential" label="新口令" rules={credentialRules}>
          <Input.Password autoComplete="new-password" />
        </Form.Item>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          重置
        </Button>
      </Form>
    </>
  )
}

// ---- 下发详情 -----------------------------------------------------------------------

const PROVISION_STATUS: Record<string, { label: string; color: string }> = {
  synced: { label: '已同步', color: 'green' },
  pending: { label: '待同步', color: 'blue' },
  failed: { label: '失败', color: 'red' },
}

interface ProvisionDrawerProps {
  user: EndUser | null
  canWrite: boolean
  onClose: () => void
  onChanged: () => void
}

/** 用户的配额概览与在各面板上的下发状态，可手动立即对账。 */
export function ProvisionDrawer({ user, canWrite, onClose, onChanged }: ProvisionDrawerProps) {
  return (
    <Drawer title={`下发详情：${user?.username ?? ''}`} open={user !== null} onClose={onClose} size="large" destroyOnHidden>
      {user !== null && <ProvisionBody user={user} canWrite={canWrite} onChanged={onChanged} />}
    </Drawer>
  )
}

function ProvisionBody({ user, canWrite, onChanged }: { user: EndUser; canWrite: boolean; onChanged: () => void }) {
  const { message } = App.useApp()
  const [isBusy, setIsBusy] = useState(false)
  const load = useCallback((signal: AbortSignal) => api.GET('/api/admin/users/{id}/provisions', { params: { path: { id: user.id } }, signal }), [user.id])
  const provisions = useRemote<Provision[]>(load)
  const reconcile = async () => {
    setIsBusy(true)
    const err = await mutate(() => api.POST('/api/admin/users/{id}/reconcile', { params: { ...csrfHeader(), path: { id: user.id } } }))
    setIsBusy(false)
    if (err !== null) {
      void message.error(err.message)
      return
    }
    void message.success('已对账')
    provisions.reload()
    onChanged()
  }
  return (
    <>
      <Descriptions size="small" column={1} bordered className="page-alert">
        <Descriptions.Item label="套餐">{user.planName || '—'}</Descriptions.Item>
        <Descriptions.Item label="已用 / 额度">{`${formatBytes(user.trafficUsed)} / ${user.trafficLimit === 0 ? '不限' : formatBytes(user.trafficLimit)}`}</Descriptions.Item>
        <Descriptions.Item label="到期">{user.expiresAt === null || user.expiresAt === undefined ? (user.pendingDays > 0 ? `首次使用后 ${user.pendingDays} 天` : '不过期') : formatTime(user.expiresAt)}</Descriptions.Item>
        <Descriptions.Item label="同时在线 IP">{user.limitIp === 0 ? '不限' : user.limitIp}</Descriptions.Item>
        <Descriptions.Item label="自动续期">{`${describeRenew(user)}（已续 ${user.renewCount} 次）`}</Descriptions.Item>
        <Descriptions.Item label="周期重置">{describeReset(user.trafficReset, user.trafficResetDay)}</Descriptions.Item>
      </Descriptions>
      <Space className="page-header">
        <Typography.Title level={5}>各面板下发</Typography.Title>
        {canWrite && (
          <Button loading={isBusy} onClick={() => void reconcile()}>
            立即对账
          </Button>
        )}
      </Space>
      {provisions.error !== null && <Alert type="error" title={provisions.error.message} showIcon className="page-alert" />}
      <Table<Provision>
        rowKey="serverId"
        size="small"
        scroll={TABLE_SCROLL}
        loading={provisions.isLoading}
        pagination={false}
        dataSource={provisions.data ?? []}
        locale={{ emptyText: '未下发到任何面板（没有套餐或套餐里没有可用节点）' }}
        columns={[
          { title: '服务器', dataIndex: 'serverName' },
          { title: '面板 client', dataIndex: 'email' },
          { title: '状态', dataIndex: 'status', render: (s: string) => <Tag color={PROVISION_STATUS[s]?.color}>{PROVISION_STATUS[s]?.label ?? s}</Tag> },
          { title: '最近同步', dataIndex: 'syncedAt', render: (v: string | null) => formatTime(v) },
          { title: '错误', dataIndex: 'lastError', render: (v: string, p: Provision) => (v === '' ? '—' : `${v}（重试 ${p.retryCount} 次）`) },
        ]}
      />
    </>
  )
}
