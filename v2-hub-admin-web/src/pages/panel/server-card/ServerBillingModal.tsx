import { Alert, Button, DatePicker, Form, InputNumber, Select } from 'antd'
import dayjs, { type Dayjs } from 'dayjs'
import { api, csrfHeader, type Server, type ServerBillingInput } from '../../../api/client'
import { ErrorAlert } from '../../../components/ErrorAlert'
import { FormModal } from '../../../components/FormModal'
import { useSubmit } from '../../../hooks/use-submit'
import { BILLING_CYCLE_LABELS, TRAFFIC_MODE_LABELS, bytesToGiB, centsToUnits, formatBytes, gibToBytes, unitsToCents } from './server-card-format'

// 取值范围与 hub-api.yaml 的 ServerBilling 一致（Ruling-84）
const MAX_PRICE_UNITS = 999999.99
const PRICE_PRECISION = 2
const GIB_PRECISION = 2
const MIN_RESET_DAY = 1
const MAX_RESET_DAY = 31
const DATE_FORMAT = 'YYYY-MM-DD'
const CURRENCY_PATTERN = /^[A-Z]{3}$/
const CURRENCIES = ['USD', 'CNY', 'EUR', 'HKD', 'JPY', 'GBP', 'SGD', 'CAD', 'AUD'] as const
const MODAL_WIDTH = 560

interface BillingForm {
  price?: number | null
  priceCurrency: string
  billingCycle: ServerBillingInput['billingCycle']
  expiresOn?: Dayjs | null
  quotaGiB?: number | null
  trafficResetDay: number
  trafficMode: ServerBillingInput['trafficMode']
  usedGiB?: number | null
}

const cycleOptions = Object.entries(BILLING_CYCLE_LABELS).map(([value, label]) => ({ value, label }))
const modeOptions = Object.entries(TRAFFIC_MODE_LABELS).map(([value, label]) => ({ value, label }))
const currencyOptions = CURRENCIES.map((c) => ({ value: c, label: c }))
const currencyRules = [{ required: true, message: '必填' }, { pattern: CURRENCY_PATTERN, message: '三位大写字母，如 USD' }]

interface ServerBillingModalProps {
  /** 要改的服务器；null 表示关着。 */
  server: Server | null
  onClose: () => void
  onSaved: () => void
}

/** 服务器的账单与流量额度（docs/03 §5.1，Ruling-84）。 */
export function ServerBillingModal({ server, onClose, onSaved }: ServerBillingModalProps) {
  return (
    <FormModal title={server === null ? '账单与流量额度' : `账单与流量额度：${server.name}`} isOpen={server !== null} onClose={onClose} width={MODAL_WIDTH}>
      {(generation) => server !== null && <ServerBillingBody key={generation} server={server} onSaved={onSaved} />}
    </FormModal>
  )
}

function initialValues(server: Server): BillingForm {
  const b = server.billing
  return {
    price: b.priceCents === null ? null : centsToUnits(b.priceCents),
    priceCurrency: b.priceCurrency,
    billingCycle: b.billingCycle,
    expiresOn: b.expiresOn === null ? null : dayjs(b.expiresOn, DATE_FORMAT),
    quotaGiB: b.trafficQuotaBytes === null ? null : bytesToGiB(b.trafficQuotaBytes),
    trafficResetDay: b.trafficResetDay,
    trafficMode: b.trafficMode,
    usedGiB: null,
  }
}

function toInput(v: BillingForm): ServerBillingInput {
  const hasValue = (n: number | null | undefined): n is number => n !== null && n !== undefined
  return {
    priceCents: hasValue(v.price) ? unitsToCents(v.price) : null,
    priceCurrency: v.priceCurrency,
    billingCycle: v.billingCycle,
    expiresOn: v.expiresOn === null || v.expiresOn === undefined ? null : v.expiresOn.format(DATE_FORMAT),
    trafficQuotaBytes: hasValue(v.quotaGiB) ? gibToBytes(v.quotaGiB) : null,
    trafficResetDay: v.trafficResetDay,
    trafficMode: v.trafficMode,
    trafficUsedBytes: hasValue(v.usedGiB) ? gibToBytes(v.usedGiB) : null,
  }
}

function ServerBillingBody({ server, onSaved }: { server: Server; onSaved: () => void }) {
  const [form] = Form.useForm<BillingForm>()
  const { error, isBusy, run } = useSubmit('已保存', onSaved)
  const submit = (v: BillingForm) =>
    run(() => api.PUT('/api/admin/servers/{id}/billing', { params: { ...csrfHeader(), path: { id: server.id } }, body: toInput(v) }))
  return (
    <Form<BillingForm> form={form} layout="vertical" initialValues={initialValues(server)} onFinish={(v) => void submit(v)}>
      <ErrorAlert error={error} />
      <Alert type="info" showIcon className="page-alert" title="只用于在卡片上展示：到期不提醒，超额不停用。日期按站点时区" />
      <div className="form-row">
        <Form.Item name="price" label="价格" extra="留空表示不填">
          <InputNumber min={0} max={MAX_PRICE_UNITS} precision={PRICE_PRECISION} className="full-width" />
        </Form.Item>
        <Form.Item name="priceCurrency" label="币种" rules={currencyRules}>
          <Select options={currencyOptions} showSearch />
        </Form.Item>
        <Form.Item name="billingCycle" label="计费周期">
          <Select options={cycleOptions} />
        </Form.Item>
      </div>
      <Form.Item name="expiresOn" label="到期日">
        <DatePicker allowClear format={DATE_FORMAT} className="full-width" />
      </Form.Item>
      <div className="form-row">
        <Form.Item name="quotaGiB" label="每月流量额度（GB）" extra="留空表示不限">
          <InputNumber min={0} precision={GIB_PRECISION} className="full-width" />
        </Form.Item>
        <Form.Item name="trafficResetDay" label="每月几号重置" extra="当月没有这一天取月末" rules={[{ required: true, message: '必填' }]}>
          <InputNumber min={MIN_RESET_DAY} max={MAX_RESET_DAY} precision={0} className="full-width" />
        </Form.Item>
        <Form.Item name="trafficMode" label="计量方式">
          <Select options={modeOptions} />
        </Form.Item>
      </div>
      <Form.Item
        name="usedGiB"
        label="校准本周期已用（GB）"
        extra={`平台从接入后才开始累计，与商家后台对不上时填这里；只对本周期有效。当前 ${formatBytes(server.traffic.usedBytes)}`}
      >
        <InputNumber min={0} precision={GIB_PRECISION} className="full-width" placeholder="不校准" />
      </Form.Item>
      <Button type="primary" htmlType="submit" loading={isBusy} block>
        保存
      </Button>
    </Form>
  )
}
