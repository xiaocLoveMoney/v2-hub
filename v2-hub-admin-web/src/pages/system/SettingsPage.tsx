import { Alert, Button, Card, Form, Input, InputNumber, Select, Space, Switch, Tag, Typography } from 'antd'
import { api, csrfHeader, type Plan, type RegisterMode, type Settings } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useRemote } from '../../hooks/use-remote'
import { useSubmit } from '../../hooks/use-submit'
import { PERMISSIONS } from '../../menu'

// 取值范围与 hub-api.yaml 的 Settings 一致（docs/03 §5.6 设置清单）
const MAX_SITE_NAME_LEN = 32
const MAX_TIMEZONE_LEN = 64
const MAX_EMAIL_DOMAINS = 100
const MAX_DOMAIN_LEN = 253
const MAX_INVITE_USES = 10000
const MAX_CHECKIN_MB = 102400
const MAX_EMAIL_LEN = 254
const MAX_MAIL_RATE = 600
const MAX_BASE_URL_LEN = 255
const MAX_SUB_TITLE_LEN = 64
const MAX_UPDATE_INTERVAL_HOURS = 168
const MAX_LOG_RETENTION_DAYS = 365
const NO_PLAN_ID = 0

const REGISTER_MODE_OPTIONS: { value: RegisterMode; label: string }[] = [
  { value: 'open', label: '开放注册' },
  { value: 'invite', label: '仅凭邀请码' },
  { value: 'closed', label: '关闭注册' },
]

/** 表单值：与 Settings 同形，去掉只读的 mailEnabled。 */
type SettingsForm = Omit<Settings, 'mailEnabled'>

const loadSettings = (signal: AbortSignal) => api.GET('/api/admin/settings', { signal })
const loadPlans = (signal: AbortSignal) => api.GET('/api/admin/plans', { signal })
const required = [{ required: true, message: '必填' }]
const domainListRules = [
  {
    validator: async (_: unknown, domains: string[] | undefined) => {
      const list = domains ?? []
      if (list.length > MAX_EMAIL_DOMAINS || list.some((d) => d.length > MAX_DOMAIN_LEN)) {
        throw new Error(`最多 ${MAX_EMAIL_DOMAINS} 个域名，每个不超过 ${MAX_DOMAIN_LEN} 个字符`)
      }
    },
  },
]

/** 系统设置（setting:read 查看，setting:write 修改）：一张表单整体保存，任一项不合法则全部不保存（docs/03 §5.6）。 */
export function SettingsPage() {
  const { can } = useSession()
  const settings = useRemote<Settings>(loadSettings)
  return (
    <>
      <Typography.Title level={3}>系统设置</Typography.Title>
      <ErrorAlert error={settings.error} />
      {settings.data !== null && (
        <SettingsFormBody
          settings={settings.data}
          canWrite={can(PERMISSIONS.SETTING_WRITE)}
          canReadPlans={can(PERMISSIONS.PLAN_READ)}
          onSaved={settings.reload}
        />
      )}
    </>
  )
}

interface SettingsFormBodyProps {
  settings: Settings
  canWrite: boolean
  canReadPlans: boolean
  onSaved: () => void
}

function SettingsFormBody({ settings, canWrite, canReadPlans, onSaved }: SettingsFormBodyProps) {
  const { error, isBusy, run } = useSubmit('已保存', onSaved)
  const { mailEnabled, ...initialValues } = settings
  const submit = (v: SettingsForm) => run(() => api.PUT('/api/admin/settings', { params: csrfHeader(), body: v }))

  return (
    <>
      <ErrorAlert error={error} />
      <Form<SettingsForm> layout="vertical" initialValues={initialValues} disabled={!canWrite} onFinish={(v) => void submit(v)}>
        <Card title="站点" className="settings-card">
          <Form.Item name={['site', 'name']} label="站点名称" rules={[{ required: true, max: MAX_SITE_NAME_LEN, message: `1–${MAX_SITE_NAME_LEN} 个字符` }]}>
            <Input autoComplete="off" />
          </Form.Item>
          <Form.Item
            name={['site', 'timezone']}
            label="平台时区"
            extra="IANA 时区名，如 Asia/Shanghai；签到与“今日流量”按它划分日期"
            rules={[{ required: true, max: MAX_TIMEZONE_LEN, message: `1–${MAX_TIMEZONE_LEN} 个字符` }]}
          >
            <Input autoComplete="off" />
          </Form.Item>
        </Card>

        <Card title="注册与邀请" className="settings-card">
          <Form.Item name={['register', 'mode']} label="注册方式" rules={required}>
            <Select options={REGISTER_MODE_OPTIONS} />
          </Form.Item>
          <Form.Item name={['register', 'emailVerify']} label="注册须验证邮箱" extra="邮件不可用时自动跳过" valuePropName="checked">
            <Switch />
          </Form.Item>
          <Form.Item name={['register', 'emailAllow']} label="允许的邮箱域名" extra="留空 = 不限；填了则只收这些域名" rules={domainListRules}>
            <Select mode="tags" open={false} tokenSeparators={[',', ' ']} />
          </Form.Item>
          <Form.Item name={['register', 'emailBlock']} label="禁止的邮箱域名" rules={domainListRules}>
            <Select mode="tags" open={false} tokenSeparators={[',', ' ']} />
          </Form.Item>
          <Form.Item name={['register', 'planId']} label="注册赠送的套餐" rules={required}>
            <RegisterPlanField canReadPlans={canReadPlans} />
          </Form.Item>
          <Form.Item name={['invite', 'userEnabled']} label="用户可以用个人邀请码邀请别人" valuePropName="checked">
            <Switch />
          </Form.Item>
          <Form.Item name={['invite', 'userMaxUses']} label="个人邀请码可用次数" extra="0 表示不限" rules={required}>
            <InputNumber min={0} max={MAX_INVITE_USES} precision={0} className="full-width" />
          </Form.Item>
        </Card>

        <Card title="签到" className="settings-card">
          <Form.Item name={['checkin', 'enabled']} label="开启签到" valuePropName="checked">
            <Switch />
          </Form.Item>
          <Form.Item name={['checkin', 'minMb']} label="奖励下限（MB）" rules={required}>
            <InputNumber min={1} max={MAX_CHECKIN_MB} precision={0} className="full-width" />
          </Form.Item>
          <Form.Item
            name={['checkin', 'maxMb']}
            label="奖励上限（MB）"
            dependencies={[['checkin', 'minMb']]}
            rules={[
              ...required,
              ({ getFieldValue }) => ({
                validator: async (_: unknown, max: number | null) => {
                  const min = getFieldValue(['checkin', 'minMb']) as number | null
                  if (max !== null && min !== null && max < min) {
                    throw new Error('上限不能小于下限')
                  }
                },
              }),
            ]}
          >
            <InputNumber min={1} max={MAX_CHECKIN_MB} precision={0} className="full-width" />
          </Form.Item>
        </Card>

        <Card title="工单与共享账号" className="settings-card">
          <Form.Item name={['ticket', 'enabled']} label="开启工单" extra="关闭后用户不能新建与回复，已有工单仍可查看" valuePropName="checked">
            <Switch />
          </Form.Item>
          <Form.Item
            name={['ticket', 'notifyEmail']}
            label="新工单通知邮箱"
            extra="留空 = 不通知；通知里不带工单正文"
            rules={[{ type: 'email', max: MAX_EMAIL_LEN, message: '邮箱格式不对' }]}
          >
            <Input autoComplete="off" />
          </Form.Item>
          <Form.Item name={['shared', 'enabled']} label="开启共享账号页" valuePropName="checked">
            <Switch />
          </Form.Item>
        </Card>

        <Card title="订阅" className="settings-card">
          <Form.Item
            name={['subscription', 'baseUrl']}
            label="订阅地址前缀"
            extra="留空 = 取请求的协议与主机；如 https://sub.example.com"
            rules={[{ max: MAX_BASE_URL_LEN }]}
          >
            <Input autoComplete="off" />
          </Form.Item>
          <Form.Item name={['subscription', 'title']} label="订阅名称" rules={[{ required: true, max: MAX_SUB_TITLE_LEN, message: `1–${MAX_SUB_TITLE_LEN} 个字符` }]}>
            <Input autoComplete="off" />
          </Form.Item>
          <Form.Item name={['subscription', 'updateIntervalHours']} label="客户端自动更新间隔（小时）" rules={required}>
            <InputNumber min={1} max={MAX_UPDATE_INTERVAL_HOURS} precision={0} className="full-width" />
          </Form.Item>
          <Form.Item name={['subscription', 'infoNodes']} label="在订阅里附带流量与到期信息节点" valuePropName="checked">
            <Switch />
          </Form.Item>
          <Form.Item name={['subscription', 'logRetentionDays']} label="订阅记录保留天数" rules={required}>
            <InputNumber min={1} max={MAX_LOG_RETENTION_DAYS} precision={0} className="full-width" />
          </Form.Item>
        </Card>

        <Card
          title={
            <Space>
              邮件
              {mailEnabled ? <Tag color="green">已配置</Tag> : <Tag>未配置</Tag>}
            </Space>
          }
          className="settings-card"
        >
          {!mailEnabled && (
            <Alert
              type="info"
              showIcon
              className="page-alert"
              title="SMTP 只能经环境变量 HUB_SMTP_* 配置；未配置时注册验证码、找回口令、换绑邮箱、公告群发与工单通知整体关闭"
            />
          )}
          <Form.Item name={['mail', 'ratePerMinute']} label="每分钟最多发送封数" rules={required}>
            <InputNumber min={1} max={MAX_MAIL_RATE} precision={0} className="full-width" />
          </Form.Item>
        </Card>

        {canWrite && (
          <Button type="primary" htmlType="submit" loading={isBusy}>
            保存全部设置
          </Button>
        )}
      </Form>
      {canWrite && <TestMailCard isMailEnabled={mailEnabled} />}
    </>
  )
}

interface RegisterPlanFieldProps {
  canReadPlans: boolean
  /** 由 Form.Item 注入。 */
  value?: number
  /** 由 Form.Item 注入。 */
  onChange?: (value: number) => void
}

/** 注册赠送的套餐：能读套餐列表时给下拉，否则只能填套餐 ID（0 = 不送）。 */
function RegisterPlanField({ canReadPlans, value, onChange }: RegisterPlanFieldProps) {
  if (!canReadPlans) {
    return <InputNumber min={NO_PLAN_ID} precision={0} className="full-width" value={value} onChange={(v) => onChange?.(v ?? NO_PLAN_ID)} aria-label="套餐 ID" />
  }
  return <RegisterPlanSelect value={value} onChange={onChange} />
}

function RegisterPlanSelect({ value, onChange }: Pick<RegisterPlanFieldProps, 'value' | 'onChange'>) {
  const plans = useRemote<Plan[]>(loadPlans)
  const options = [{ value: NO_PLAN_ID, label: '不送套餐' }, ...(plans.data ?? []).map((p) => ({ value: p.id, label: p.name }))]
  return <Select loading={plans.isLoading} options={options} value={value} onChange={onChange} optionFilterProp="label" showSearch />
}

/** 向指定地址发一封测试邮件，直接显示 SMTP 的结果（docs/03 §5.7）。 */
function TestMailCard({ isMailEnabled }: { isMailEnabled: boolean }) {
  const [form] = Form.useForm<{ to: string }>()
  const { error, isBusy, run } = useSubmit('测试邮件已发出', () => form.resetFields())
  const submit = (v: { to: string }) => run(() => api.POST('/api/admin/settings/test-mail', { params: csrfHeader(), body: { to: v.to } }))
  return (
    <Card title="发测试邮件" className="settings-card settings-card-extra">
      <ErrorAlert error={error} />
      <Form form={form} layout="inline" disabled={!isMailEnabled} onFinish={(v) => void submit(v)}>
        <Form.Item name="to" rules={[{ required: true, type: 'email', max: MAX_EMAIL_LEN, message: '请输入收件邮箱' }]}>
          <Input autoComplete="off" placeholder="收件邮箱" aria-label="收件邮箱" className="filter-select" />
        </Form.Item>
        <Button htmlType="submit" loading={isBusy}>
          发送
        </Button>
      </Form>
    </Card>
  )
}
