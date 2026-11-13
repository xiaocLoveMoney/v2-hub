import { PlusOutlined } from '@ant-design/icons'
import { Button, DatePicker, Form, Input, InputNumber, Modal, Popconfirm, Radio, Select, Space, Switch, Tag, Typography, type TableProps } from 'antd'
import dayjs, { type Dayjs } from 'dayjs'
import { useCallback, useState } from 'react'
import { api, csrfHeader, type Coupon, type CouponInput, type CouponKind, type Plan } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { ErrorAlert } from '../../components/ErrorAlert'
import { PagedTable } from '../../components/PagedTable'
import { useCurrency } from '../../hooks/use-currency'
import { useOpenGeneration } from '../../hooks/use-open-generation'
import { useRemote } from '../../hooks/use-remote'
import { useRowAction, useSubmit } from '../../hooks/use-submit'
import { PERMISSIONS } from '../../menu'
import { MAX_MONEY_AMOUNT, MONEY_PRECISION, amountToCents, centsToAmount, formatMoney } from '../../money'
import { formatTime } from '../system/format'

const COUPON_CODE_PATTERN = /^[A-Za-z0-9]{4,32}$/
const MAX_PERCENT = 100
const MAX_USES = 1000000
const MAX_USES_PER_USER = 1000
const MAX_REMARK_LEN = 255
const UNLIMITED = 0
const MODAL_WIDTH = 620

const KIND_OPTIONS: { value: CouponKind; label: string }[] = [
  { value: 'percent', label: '按比例' },
  { value: 'fixed', label: '固定金额' },
]

/** 表单值：percent 填百分比，fixed 与最低消费按元输入。 */
interface CouponForm {
  code: string
  kind: CouponKind
  percent: number
  amount: number
  allPlans: boolean
  planIds: number[]
  forTrafficPack: boolean
  minAmount: number
  maxUses: number
  maxUsesPerUser: number
  startsAt?: Dayjs | null
  endsAt?: Dayjs | null
  isDisabled: boolean
  remark: string
}

const NO_PLANS: Plan[] = []
const loadPlans = (signal: AbortSignal) => api.GET('/api/admin/plans', { signal })
// 没有 plan:read 时不请求套餐列表，指定套餐只显示 ID
const loadNoPlans = () => Promise.resolve({ data: { data: NO_PLANS } })

function formOf(c: Coupon | null): CouponForm {
  if (c === null) {
    return {
      code: '', kind: 'percent', percent: 10, amount: 0, allPlans: true, planIds: [], forTrafficPack: false, minAmount: 0, maxUses: UNLIMITED,
      maxUsesPerUser: 1, startsAt: null, endsAt: null, isDisabled: false, remark: '',
    }
  }
  return {
    code: c.code, kind: c.kind, percent: c.kind === 'percent' ? c.value : 10, amount: c.kind === 'fixed' ? centsToAmount(c.value) : 0,
    allPlans: c.allPlans, planIds: c.planIds ?? [], forTrafficPack: c.forTrafficPack ?? false, minAmount: centsToAmount(c.minAmount ?? 0),
    maxUses: c.maxUses ?? UNLIMITED, maxUsesPerUser: c.maxUsesPerUser ?? 1, startsAt: c.startsAt ? dayjs(c.startsAt) : null,
    endsAt: c.endsAt ? dayjs(c.endsAt) : null, isDisabled: c.isDisabled, remark: c.remark ?? '',
  }
}

function inputOf(v: CouponForm): CouponInput {
  return {
    code: v.code.toUpperCase(), kind: v.kind, value: v.kind === 'percent' ? v.percent : amountToCents(v.amount), allPlans: v.allPlans,
    planIds: v.allPlans ? [] : (v.planIds ?? []), forTrafficPack: v.forTrafficPack, minAmount: amountToCents(v.minAmount ?? 0), maxUses: v.maxUses ?? UNLIMITED,
    maxUsesPerUser: v.maxUsesPerUser ?? UNLIMITED, startsAt: v.startsAt ? v.startsAt.toISOString() : null, endsAt: v.endsAt ? v.endsAt.toISOString() : null,
    isDisabled: v.isDisabled, remark: v.remark ?? '',
  }
}

/** 优惠码（shop:read 查看，shop:write 增改删）：不能用于试用套餐与自动续费；用过的只能停用（docs/03 §5.15）。 */
export function CouponsPage() {
  const { can } = useSession()
  const canWrite = can(PERMISSIONS.SHOP_WRITE)
  const currency = useCurrency()
  const plans = useRemote<Plan[]>(can(PERMISSIONS.PLAN_READ) ? loadPlans : loadNoPlans)
  const [refreshKey, setRefreshKey] = useState(0)
  const reload = () => setRefreshKey((k) => k + 1)
  const act = useRowAction(reload)
  const [editing, setEditing] = useState<Coupon | null>(null)
  const [isOpen, setIsOpen] = useState(false)
  const load = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) => api.GET('/api/admin/coupons', { params: { query: { page, pageSize } }, signal }),
    [],
  )
  const planName = new Map((plans.data ?? NO_PLANS).map((p) => [p.id, p.name]))

  const open = (c: Coupon | null) => {
    setEditing(c)
    setIsOpen(true)
  }

  const columns: TableProps<Coupon>['columns'] = [
    { title: '优惠码', dataIndex: 'code', render: (code: string) => <Typography.Text copyable className="mono">{code}</Typography.Text> },
    { title: '优惠', key: 'value', render: (_: unknown, c: Coupon) => (c.kind === 'percent' ? `${c.value}%` : formatMoney(c.value, currency)) },
    {
      title: '适用范围',
      key: 'scope',
      render: (_: unknown, c: Coupon) => (
        <Space size={4} wrap>
          {c.allPlans ? <Tag>全部套餐</Tag> : (c.planIds ?? []).map((id) => <Tag key={id}>{planName.get(id) ?? `#${id}`}</Tag>)}
          {c.forTrafficPack === true && <Tag color="blue">流量包</Tag>}
        </Space>
      ),
    },
    { title: '已用 / 上限', key: 'uses', render: (_: unknown, c: Coupon) => `${c.usedCount} / ${(c.maxUses ?? UNLIMITED) === UNLIMITED ? '不限' : c.maxUses}` },
    { title: '失效时间', dataIndex: 'endsAt', render: (v: string | null | undefined) => (v ? formatTime(v) : '不过期') },
    { title: '状态', dataIndex: 'isDisabled', render: (isDisabled: boolean) => (isDisabled ? <Tag color="red">已停用</Tag> : <Tag color="green">可用</Tag>) },
  ]
  if (canWrite) {
    columns.push({
      title: '操作',
      key: 'actions',
      render: (_: unknown, c: Coupon) => (
        <Space>
          <Button size="small" onClick={() => open(c)}>
            编辑
          </Button>
          {c.usedCount === 0 && (
            <Popconfirm
              title={`删除优惠码 ${c.code}？`}
              onConfirm={() => act(() => api.DELETE('/api/admin/coupons/{id}', { params: { ...csrfHeader(), path: { id: c.id } } }), '已删除')}
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
      <Space className="page-header">
        <Typography.Title level={3}>优惠码</Typography.Title>
        {canWrite && (
          <Button type="primary" icon={<PlusOutlined />} onClick={() => open(null)}>
            新建优惠码
          </Button>
        )}
      </Space>
      <PagedTable<Coupon> load={load} columns={columns} refreshKey={refreshKey} />
      <CouponModal
        coupon={editing}
        isOpen={isOpen}
        plans={plans.data ?? NO_PLANS}
        onClose={() => setIsOpen(false)}
        onSaved={() => {
          setIsOpen(false)
          reload()
        }}
      />
    </>
  )
}

interface CouponModalProps {
  /** null = 新建。 */
  coupon: Coupon | null
  isOpen: boolean
  plans: Plan[]
  onClose: () => void
  onSaved: () => void
}

function CouponModal({ coupon, isOpen, plans, onClose, onSaved }: CouponModalProps) {
  const generation = useOpenGeneration(isOpen)
  return (
    <Modal title={coupon === null ? '新建优惠码' : `编辑优惠码：${coupon.code}`} open={isOpen} onCancel={onClose} footer={null} destroyOnHidden width={MODAL_WIDTH}>
      <CouponFormBody key={generation} coupon={coupon} plans={plans} onSaved={onSaved} />
    </Modal>
  )
}

function CouponFormBody({ coupon, plans, onSaved }: Pick<CouponModalProps, 'coupon' | 'plans' | 'onSaved'>) {
  const { error, isBusy, run } = useSubmit(coupon === null ? '已创建优惠码' : '已保存', onSaved)
  const [form] = Form.useForm<CouponForm>()
  const kind = Form.useWatch('kind', form)
  const isAllPlans = Form.useWatch('allPlans', form)
  const submit = (v: CouponForm) => {
    const body = inputOf(v)
    return run(() =>
      coupon === null
        ? api.POST('/api/admin/coupons', { params: csrfHeader(), body })
        : api.PUT('/api/admin/coupons/{id}', { params: { ...csrfHeader(), path: { id: coupon.id } }, body }),
    )
  }
  return (
    <>
      <ErrorAlert error={error} />
      <Form<CouponForm> form={form} layout="vertical" initialValues={formOf(coupon)} onFinish={(v) => void submit(v)}>
        <Form.Item name="code" label="优惠码" extra="4–32 位字母或数字，不区分大小写" rules={[{ required: true, pattern: COUPON_CODE_PATTERN, message: '4–32 位字母或数字' }]}>
          <Input autoComplete="off" className="mono" />
        </Form.Item>
        <Form.Item name="kind" label="优惠方式">
          <Radio.Group optionType="button" options={KIND_OPTIONS} />
        </Form.Item>
        {kind === 'fixed' ? (
          <Form.Item name="amount" label="减免金额" extra="超过原价时按原价减免" rules={[{ required: true, message: '请填金额' }]}>
            <InputNumber min={0.01} max={MAX_MONEY_AMOUNT} precision={MONEY_PRECISION} className="full-width" />
          </Form.Item>
        ) : (
          <Form.Item name="percent" label="减免比例（%）" extra="优惠 = 原价 × 比例，向下取整到分" rules={[{ required: true, message: '请填比例' }]}>
            <InputNumber min={1} max={MAX_PERCENT} precision={0} className="full-width" />
          </Form.Item>
        )}
        <Form.Item name="allPlans" label="适用于全部套餐" valuePropName="checked">
          <Switch />
        </Form.Item>
        {isAllPlans === false && (
          <Form.Item name="planIds" label="指定套餐" rules={[{ required: true, message: '请选择套餐' }]}>
            <Select mode="multiple" optionFilterProp="label" options={plans.map((p) => ({ value: p.id, label: p.name }))} />
          </Form.Item>
        )}
        <Form.Item name="forTrafficPack" label="也适用于流量包" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name="minAmount" label="最低消费" extra="原价不低于这个金额才能用；0 = 不限">
          <InputNumber min={0} max={MAX_MONEY_AMOUNT} precision={MONEY_PRECISION} className="full-width" />
        </Form.Item>
        <Form.Item name="maxUses" label="总次数上限" extra="0 = 不限">
          <InputNumber min={0} max={MAX_USES} precision={0} className="full-width" />
        </Form.Item>
        <Form.Item name="maxUsesPerUser" label="每人次数上限" extra="0 = 不限">
          <InputNumber min={0} max={MAX_USES_PER_USER} precision={0} className="full-width" />
        </Form.Item>
        <Form.Item name="startsAt" label="生效时间" extra="留空 = 立即生效">
          <DatePicker showTime allowClear className="full-width" />
        </Form.Item>
        <Form.Item name="endsAt" label="失效时间" extra="留空 = 不过期">
          <DatePicker showTime allowClear className="full-width" />
        </Form.Item>
        <Form.Item name="remark" label="备注" rules={[{ max: MAX_REMARK_LEN }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="isDisabled" label="停用" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          {coupon === null ? '创建' : '保存'}
        </Button>
      </Form>
    </>
  )
}
