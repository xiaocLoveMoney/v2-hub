import { Alert, Button, Card, Form, Input, InputNumber, Select, Space, Switch, Tag } from 'antd'
import { api, csrfHeader } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useSubmit } from '../../hooks/use-submit'

// 运营设置（docs/03 §5.20）；取值范围与 hub-api.yaml 的 Settings 一致
const MAX_ALERT_EMAILS = 10
const MAX_EMAIL_LEN = 254
const MAX_OFFLINE_MINUTES = 60
const MAX_REPORT_HOUR = 23
const MAX_CHAT_ID_LEN = 64
const MAX_HEADLINE_LEN = 64
const MAX_INTRO_LEN = 2000
const MAX_SUPPORT_URL_LEN = 255
const MAX_SUPPORT_LABEL_LEN = 16
const INTRO_ROWS = 6
/** Telegram 会话 id：数字（群组为负数）或 @频道用户名；与后端校验一致。 */
const CHAT_ID_PATTERN = /^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})?$/
const HTTPS_PREFIX = 'https://'
const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

const required = [{ required: true, message: '必填' }]
const noop = () => undefined
const alertEmailRules = [
  {
    validator: async (_: unknown, emails: string[] | undefined) => {
      const list = emails ?? []
      if (list.length > MAX_ALERT_EMAILS || list.some((e) => e.length > MAX_EMAIL_LEN || !EMAIL_PATTERN.test(e))) {
        throw new Error(`最多 ${MAX_ALERT_EMAILS} 个邮箱，格式要正确`)
      }
    },
  },
]
const supportURLRules = [
  { max: MAX_SUPPORT_URL_LEN },
  {
    validator: async (_: unknown, url: string | undefined) => {
      if (url !== undefined && url !== '' && !url.startsWith(HTTPS_PREFIX)) {
        throw new Error('只接受 https:// 开头的地址')
      }
    },
  },
]

/** 掉线告警、Telegram、日报、落地页、客服入口与服务器路由的设置卡片；放在系统设置的同一张表单里整体保存。 */
export function OpsSettingsCards({ isTelegramEnabled }: { isTelegramEnabled: boolean }) {
  return (
    <>
      <Card title="掉线告警与日报" className="settings-card">
        <Form.Item name={['alert', 'emails']} label="告警与日报收件人" extra="掉线告警、恢复通知与运营日报发到这些邮箱（需邮件可用）" rules={alertEmailRules}>
          <Select mode="tags" open={false} tokenSeparators={[',', ' ']} />
        </Form.Item>
        <Form.Item name={['alert', 'offlineMinutes']} label="连续不在线多少分钟告警" extra="一次掉线只告警一次，恢复时再发一条恢复通知" rules={required}>
          <InputNumber min={1} max={MAX_OFFLINE_MINUTES} precision={0} className="full-width" />
        </Form.Item>
        <Form.Item name={['report', 'dailyEnabled']} label="每天发运营日报" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name={['report', 'dailyHour']} label="日报发送时刻（点）" extra="按平台时区；发的是前一天的数据" rules={required}>
          <InputNumber min={0} max={MAX_REPORT_HOUR} precision={0} className="full-width" />
        </Form.Item>
      </Card>

      <Card
        title={
          <Space>
            Telegram
            {isTelegramEnabled ? <Tag color="green">已配置 token</Tag> : <Tag>未配置 token</Tag>}
          </Space>
        }
        className="settings-card"
      >
        {!isTelegramEnabled && (
          <Alert type="info" showIcon className="page-alert" title="机器人 token 只能经环境变量 HUB_TELEGRAM_BOT_TOKEN 配置；只给管理员发通知" />
        )}
        <Form.Item
          name={['telegram', 'chatId']}
          label="会话 id"
          extra="数字（群组为负数）或 @频道用户名；留空 = 不发"
          rules={[{ max: MAX_CHAT_ID_LEN }, { pattern: CHAT_ID_PATTERN, message: '会话 id 格式不对' }]}
        >
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name={['telegram', 'notifyAlerts']} label="发掉线与恢复通知" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name={['telegram', 'notifyTickets']} label="发工单通知" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name={['telegram', 'notifyPayments']} label="发在线充值入账通知" valuePropName="checked">
          <Switch />
        </Form.Item>
      </Card>

      <Card title="落地页与客服入口" className="settings-card">
        <Form.Item name={['landing', 'enabled']} label="开启落地页" extra="未登录访问客户门户时显示；商店开启时展示在售套餐" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name={['landing', 'headline']} label="落地页标题" extra="留空 = 用站点名" rules={[{ max: MAX_HEADLINE_LEN }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name={['landing', 'intro']} label="落地页介绍（Markdown）" extra="渲染规则同公告：不解析 HTML" rules={[{ max: MAX_INTRO_LEN }]}>
          <Input.TextArea rows={INTRO_ROWS} />
        </Form.Item>
        <Form.Item name={['support', 'url']} label="客服链接" extra="https:// 外链，新窗口打开；留空 = 不显示客服按钮。不支持嵌入脚本" rules={supportURLRules}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name={['support', 'label']} label="客服按钮文字" rules={[{ required: true, max: MAX_SUPPORT_LABEL_LEN, message: `1–${MAX_SUPPORT_LABEL_LEN} 个字符` }]}>
          <Input autoComplete="off" />
        </Form.Item>
      </Card>

      <Card title="服务器路由" className="settings-card">
        {/* docs/03 §5.19 拦截规则，Ruling-55 */}
        <Form.Item
          name={['routing', 'blockRule']}
          label="平台拦截规则"
          extra="在每台已接入服务器的 Xray 模板里加一条拦截规则 hub-block：广告、360、百度地图上报、临时邮箱、迅雷、部分网站与支付 / 游戏点卡站、MetaTrader。开或关之后每台服务器会重启一次 Xray（断开现有连接）"
          valuePropName="checked"
        >
          <Switch />
        </Form.Item>
      </Card>
    </>
  )
}

/** 向设置里的 Telegram 会话发一条测试消息（docs/03 §5.20）；要先保存会话 id。 */
export function TestTelegramCard({ isTelegramEnabled }: { isTelegramEnabled: boolean }) {
  const { error, isBusy, run } = useSubmit('测试消息已发出', noop)
  const send = () => run(() => api.POST('/api/admin/settings/test-telegram', { params: csrfHeader() }))
  return (
    <Card title="发 Telegram 测试消息" className="settings-card">
      <ErrorAlert error={error} />
      <Button onClick={() => void send()} loading={isBusy} disabled={!isTelegramEnabled}>
        发送到已保存的会话
      </Button>
    </Card>
  )
}
