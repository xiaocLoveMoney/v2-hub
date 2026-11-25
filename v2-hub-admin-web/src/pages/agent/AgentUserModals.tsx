import { Alert, Button, DatePicker, Descriptions, Form, Input, InputNumber, Select, Switch } from 'antd'
import dayjs, { type Dayjs } from 'dayjs'
import { useState } from 'react'
import { api, csrfHeader, type AgentPlan, type AgentSummary, type AgentUser, type ApiError } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { FormModal } from '../../components/FormModal'
import { useSubmit } from '../../hooks/use-submit'
import { MAX_REMARK_LEN } from '../form-limits'
import { formatTime } from '../system/format'
import { bytesToGb, describeExpiry, formatBytes, gbToBytes } from '../users/quota'
import { USERNAME_PATTERN, credentialRules, emailRules } from '../users/user-form-rules'
import { describeAgentPlan, withAgentErrors } from './agent-format'
import { PoolDeduction } from './PoolDeduction'
import { checkQuotaLowering } from './quota-lowering'

// 代理对名下用户的弹窗（docs/03 §5.23 代理能做的）；只调 /api/admin/agent/users/*（Ruling-101）

/** 额度输入框的步长与小数位（GB）。 */
const GB_PRECISION = 2
const MIN_TRAFFIC_GB = 0.01

function planOptions(plans: AgentPlan[]) {
  return plans.map((p) => ({ value: p.id, label: `${p.name}（${describeAgentPlan(p)}，扣减 ${formatBytes(p.poolCost)}）` }))
}

interface PlanContext {
  /** 白名单套餐。 */
  plans: AgentPlan[]
  /** 配额，用来显示池里剩余；还没取到为 null。 */
  summary: AgentSummary | null
}

// ---- 新建 -------------------------------------------------------------------------

interface CreateForm {
  username: string
  credential: string
  email?: string
  remark?: string
  planId?: number
}

interface CreateModalProps extends PlanContext {
  isOpen: boolean
  onClose: () => void
  onSaved: () => void
}

/** 新建名下用户，必须同时选一个白名单套餐（扣流量池，Ruling-103）。 */
export function CreateAgentUserModal({ isOpen, plans, summary, onClose, onSaved }: CreateModalProps) {
  return (
    <FormModal title="新建用户" isOpen={isOpen} onClose={onClose}>
      {(generation) => <CreateForm key={generation} plans={plans} summary={summary} onSaved={onSaved} />}
    </FormModal>
  )
}

function CreateForm({ plans, summary, onSaved }: PlanContext & { onSaved: () => void }) {
  const [form] = Form.useForm<CreateForm>()
  const planId = Form.useWatch('planId', form)
  const { error, isBusy, run } = useSubmit('已创建用户，正在下发到各面板', onSaved)
  const submit = (v: CreateForm) => {
    const pw = v.credential
    if (v.planId === undefined) {
      return
    }
    const body = { username: v.username, password: pw, email: v.email || undefined, remark: v.remark ?? '', planId: v.planId }
    return run(withAgentErrors(() => api.POST('/api/admin/agent/users', { params: csrfHeader(), body })))
  }
  return (
    <>
      <ErrorAlert error={error} />
      <Form<CreateForm> form={form} layout="vertical" onFinish={(v) => void submit(v)}>
        <Form.Item name="username" label="用户名" extra="登录客户后台用" rules={[{ required: true, pattern: USERNAME_PATTERN, message: '3–64 位字母、数字或 _ . -' }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="email" label="邮箱" extra="可选" rules={emailRules}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="credential" label="初始密码" rules={credentialRules}>
          <Input.Password autoComplete="new-password" />
        </Form.Item>
        <Form.Item name="planId" label="套餐" extra="必须选一个；只能选平台分配给你的套餐" rules={[{ required: true, message: '请选择套餐' }]}>
          <Select options={planOptions(plans)} notFoundContent="平台还没有给你分配套餐" />
        </Form.Item>
        <PoolDeduction plan={plans.find((p) => p.id === planId)} summary={summary} />
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

// ---- 分配 / 续期套餐 -----------------------------------------------------------------

interface UserModalProps {
  user: AgentUser | null
  onClose: () => void
  onSaved: () => void
}

/** 分配或续期白名单套餐：未到期的顺延、已到期的从当时起算、已用流量清零；每次都扣流量池（Ruling-103）。 */
export function AssignAgentPlanModal({ user, plans, summary, onClose, onSaved }: UserModalProps & PlanContext) {
  return (
    <FormModal title={`分配套餐：${user?.username ?? ''}`} isOpen={user !== null} onClose={onClose}>
      {(generation) => user !== null && <AssignForm key={generation} user={user} plans={plans} summary={summary} onSaved={onSaved} />}
    </FormModal>
  )
}

function AssignForm({ user, plans, summary, onSaved }: PlanContext & { user: AgentUser; onSaved: () => void }) {
  const [form] = Form.useForm<{ planId?: number }>()
  const planId = Form.useWatch('planId', form)
  const { error, isBusy, run } = useSubmit('已分配套餐', onSaved)
  const isWhitelisted = plans.some((p) => p.id === user.planId)
  const submit = (v: { planId?: number }) => {
    if (v.planId === undefined) {
      return
    }
    const body = { planId: v.planId }
    return run(withAgentErrors(() => api.POST('/api/admin/agent/users/{id}/plan', { params: { ...csrfHeader(), path: { id: user.id } }, body })))
  }
  return (
    <>
      <ErrorAlert error={error} />
      <Descriptions size="small" column={1} className="page-alert">
        <Descriptions.Item label="当前套餐">{user.planName || '—'}</Descriptions.Item>
        <Descriptions.Item label="当前到期">{describeExpiry(user)}</Descriptions.Item>
        <Descriptions.Item label="已用">{formatBytes(user.trafficUsed)}</Descriptions.Item>
      </Descriptions>
      <Form form={form} layout="vertical" initialValues={{ planId: isWhitelisted ? (user.planId ?? undefined) : undefined }} onFinish={(v) => void submit(v)}>
        <Form.Item name="planId" label="套餐" extra="未到期从原到期日顺延，已到期从现在起算；已用流量清零" rules={[{ required: true, message: '请选择套餐' }]}>
          <Select options={planOptions(plans)} notFoundContent="平台还没有给你分配套餐" />
        </Form.Item>
        <PoolDeduction plan={plans.find((p) => p.id === planId)} summary={summary} />
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          分配
        </Button>
      </Form>
    </>
  )
}

// ---- 编辑（备注、停用） ----------------------------------------------------------------

interface EditForm {
  remark?: string
  isDisabled: boolean
}

/** 改名下用户的备注与启用 / 停用。 */
export function EditAgentUserModal({ user, onClose, onSaved }: UserModalProps) {
  return (
    <FormModal title={`编辑用户：${user?.username ?? ''}`} isOpen={user !== null} onClose={onClose}>
      {(generation) => user !== null && <EditForm key={generation} user={user} onSaved={onSaved} />}
    </FormModal>
  )
}

function EditForm({ user, onSaved }: { user: AgentUser; onSaved: () => void }) {
  const { error, isBusy, run } = useSubmit('已保存', onSaved)
  const submit = (v: EditForm) => {
    const body = { remark: v.remark ?? '', isDisabled: v.isDisabled }
    return run(withAgentErrors(() => api.PUT('/api/admin/agent/users/{id}', { params: { ...csrfHeader(), path: { id: user.id } }, body })))
  }
  return (
    <>
      <ErrorAlert error={error} />
      <Form<EditForm> layout="vertical" initialValues={{ remark: user.remark, isDisabled: user.isDisabled }} onFinish={(v) => void submit(v)}>
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

// ---- 调低额度 / 提前到期 ----------------------------------------------------------------

interface LowerForm {
  /** 留空 = 不改。 */
  trafficLimitGb?: number | null
  /** 留空 = 不改。 */
  expiresAt?: Dayjs | null
}

/** 表单初值：不限额度与不过期都留空（留空即不改）。 */
function lowerFormOf(u: AgentUser): LowerForm {
  return {
    trafficLimitGb: u.trafficLimit === 0 ? null : bytesToGb(u.trafficLimit),
    expiresAt: u.expiresAt === null || u.expiresAt === undefined ? null : dayjs(u.expiresAt),
  }
}

/** 调低流量额度、把到期时间提前（只能收紧，Ruling-103）；要加量只能再分配一次套餐。 */
export function LowerQuotaModal({ user, onClose, onSaved }: UserModalProps) {
  return (
    <FormModal title={`调低额度：${user?.username ?? ''}`} isOpen={user !== null} onClose={onClose}>
      {(generation) => user !== null && <LowerForm key={generation} user={user} onSaved={onSaved} />}
    </FormModal>
  )
}

function LowerForm({ user, onSaved }: { user: AgentUser; onSaved: () => void }) {
  const { error, isBusy, run } = useSubmit('已保存，正在同步到各面板', onSaved)
  const [checkError, setCheckError] = useState<ApiError | null>(null)
  const initial = lowerFormOf(user)
  const isPendingFirstUse = (user.expiresAt === null || user.expiresAt === undefined) && user.pendingDays > 0
  const submit = (v: LowerForm) => {
    // 与初值相同的项视为没动：GB 回显有两位小数的舍入，不能拿舍入后的值去和字节比
    const gb = v.trafficLimitGb ?? null
    const isTrafficTouched = gb !== null && gb !== initial.trafficLimitGb
    const expiresAt = v.expiresAt ?? null
    const isExpiryTouched = expiresAt !== null && (initial.expiresAt === null || initial.expiresAt === undefined || !expiresAt.isSame(initial.expiresAt))
    const result = checkQuotaLowering(user, {
      trafficLimit: isTrafficTouched ? gbToBytes(gb) : null,
      expiresAt: isExpiryTouched ? expiresAt.toISOString() : null,
    })
    if (result.error !== null) {
      setCheckError({ code: 'invalid_argument', message: result.error })
      return
    }
    setCheckError(null)
    const body = result.body
    return run(withAgentErrors(() => api.PUT('/api/admin/agent/users/{id}/quota', { params: { ...csrfHeader(), path: { id: user.id } }, body })))
  }
  return (
    <>
      <Alert type="info" showIcon className="page-alert" title="只能调低，不能调高或设为不限" description="额度只能改小、到期时间只能提前；要加量请再分配一次套餐。" />
      <ErrorAlert error={checkError ?? error} />
      <Form<LowerForm> layout="vertical" initialValues={initial} onFinish={(v) => void submit(v)}>
        <Form.Item
          name="trafficLimitGb"
          label="流量额度（GB）"
          extra={`当前 ${user.trafficLimit === 0 ? '不限' : formatBytes(user.trafficLimit)}，已用 ${formatBytes(user.trafficUsed)}；须大于 0 且不高于当前${user.trafficLimit === 0 ? '；留空表示不改' : ''}`}
        >
          <InputNumber min={MIN_TRAFFIC_GB} precision={GB_PRECISION} className="full-width" aria-label="流量额度（GB）" />
        </Form.Item>
        <Form.Item
          name="expiresAt"
          label="到期时间"
          extra={isPendingFirstUse ? `用户还在等首次使用（首次使用后 ${user.pendingDays} 天），不能改到期时间` : `当前 ${user.expiresAt ? formatTime(user.expiresAt) : '不过期'}；只能提前，不能清空`}
        >
          <DatePicker showTime allowClear={false} className="full-width" disabled={isPendingFirstUse} aria-label="到期时间" />
        </Form.Item>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          保存
        </Button>
      </Form>
    </>
  )
}

// ---- 重置密码 -----------------------------------------------------------------------

/** 重置名下用户的客户后台登录密码（高危，后端要求二次验证，由 api 客户端统一弹框）。 */
export function ResetAgentUserCredentialModal({ user, onClose, onSaved }: UserModalProps) {
  return (
    <FormModal title={`重置密码：${user?.username ?? ''}`} isOpen={user !== null} onClose={onClose}>
      {(generation) => user !== null && <ResetCredentialForm key={generation} user={user} onSaved={onSaved} />}
    </FormModal>
  )
}

function ResetCredentialForm({ user, onSaved }: { user: AgentUser; onSaved: () => void }) {
  const { error, isBusy, run } = useSubmit('密码已重置', onSaved)
  const submit = (v: { credential: string }) => {
    const pw = v.credential
    return run(withAgentErrors(() => api.PUT('/api/admin/agent/users/{id}/password', { params: { ...csrfHeader(), path: { id: user.id } }, body: { password: pw } })))
  }
  return (
    <>
      <ErrorAlert error={error} />
      <Form layout="vertical" onFinish={(v) => void submit(v)}>
        <Form.Item name="credential" label="新密码" rules={credentialRules}>
          <Input.Password autoComplete="new-password" />
        </Form.Item>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          重置
        </Button>
      </Form>
    </>
  )
}
