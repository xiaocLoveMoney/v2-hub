import { MinusCircleOutlined, PlusOutlined } from '@ant-design/icons'
import { Alert, App, Button, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Switch, Table, Tag, Typography, type TableProps } from 'antd'
import { useState } from 'react'
import { api, csrfHeader, type ApiError, type NodeGroup, type Plan, type PlanInput } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { useCurrency } from '../../hooks/use-currency'
import { useOpenGeneration } from '../../hooks/use-open-generation'
import { mutate, useRemote } from '../../hooks/use-remote'
import { PERMISSIONS } from '../../menu'
import { MAX_MONEY_AMOUNT, MONEY_PRECISION, amountToCents, centsToAmount, formatMoney } from '../../money'
import { TABLE_SCROLL } from '../system/format'
import { ResetFields, type ResetForm } from './quota-fields'
import { bytesToGb, describeReset, formatBytes, gbToBytes } from './quota'

const MAX_NAME_LEN = 64
const MAX_DURATION_DAYS = 36500
const MAX_LIMIT_IP = 1000
// 售卖属性的取值范围与 hub-api.yaml 的 PlanBase / PlanPriceInput 一致（docs/03 §5.12）
const MAX_PRICES = 6
const MAX_PRICE_DAYS = 3650
const MAX_SORT_ORDER = 100000
const MAX_DESCRIPTION_LEN = 500
const DESCRIPTION_ROWS = 3
const MODAL_WIDTH = 620

/** 表单里的一个时长选项：金额按“元”输入，提交时换成分。 */
interface PriceRow {
  durationDays: number
  amount: number
}

interface PlanForm extends ResetForm {
  name: string
  trafficGb: number
  durationDays: number
  startOnFirstUse: boolean
  limitIp: number
  nodeGroupIds: number[]
  allowClientChain: boolean
  allowCustomNode: boolean
  remark: string
  isOnSale: boolean
  isTrial: boolean
  sortOrder: number
  description: string
  prices: PriceRow[]
}

const loadPlans = (signal: AbortSignal) => api.GET('/api/admin/plans', { signal })
const loadGroups = (signal: AbortSignal) => api.GET('/api/admin/node-groups', { signal })

function formOf(p: Plan | null): PlanForm {
  if (p === null) {
    return {
      name: '', trafficGb: 0, durationDays: 30, startOnFirstUse: false, limitIp: 0, nodeGroupIds: [], allowClientChain: false,
      allowCustomNode: false, remark: '', trafficReset: 'never', trafficResetDay: 1, isOnSale: false, isTrial: false, sortOrder: 0,
      description: '', prices: [],
    }
  }
  return {
    name: p.name, trafficGb: bytesToGb(p.trafficLimit), durationDays: p.durationDays, startOnFirstUse: p.startOnFirstUse ?? false,
    limitIp: p.limitIp ?? 0, nodeGroupIds: p.nodeGroupIds, allowClientChain: p.allowClientChain ?? false, allowCustomNode: p.allowCustomNode ?? false,
    remark: p.remark ?? '', trafficReset: p.trafficReset ?? 'never', trafficResetDay: p.trafficResetDay ?? 1,
    isOnSale: p.isOnSale ?? false, isTrial: p.isTrial ?? false, sortOrder: p.sortOrder ?? 0, description: p.description ?? '',
    prices: p.prices.map((price) => ({ durationDays: price.durationDays, amount: centsToAmount(price.price) })),
  }
}

function inputOf(v: PlanForm): PlanInput {
  return {
    name: v.name, trafficLimit: gbToBytes(v.trafficGb), durationDays: v.durationDays ?? 0, startOnFirstUse: v.startOnFirstUse, limitIp: v.limitIp ?? 0,
    trafficReset: v.trafficReset, trafficResetDay: v.trafficResetDay ?? 1, allowClientChain: v.allowClientChain, allowCustomNode: v.allowCustomNode,
    remark: v.remark ?? '', nodeGroupIds: v.nodeGroupIds ?? [],
    isOnSale: v.isOnSale, isTrial: v.isTrial, sortOrder: v.sortOrder ?? 0, description: v.description ?? '',
    // 时长选项整体替换；金额在这里从元换成整数分
    prices: (v.prices ?? []).map((row) => ({ durationDays: row.durationDays, price: amountToCents(row.amount) })),
  }
}

/** 套餐管理（plan:read 查看，plan:write 增删改）：额度模板 + 售卖属性与时长选项；改套餐不影响已分配的用户（docs/03 §5.2、§5.12）。 */
export function PlansPage() {
  const { can } = useSession()
  const { message } = App.useApp()
  const canWrite = can(PERMISSIONS.PLAN_WRITE)
  const currency = useCurrency()
  const plans = useRemote<Plan[]>(loadPlans)
  const groups = useRemote<NodeGroup[]>(loadGroups)
  const [editing, setEditing] = useState<Plan | null>(null)
  const [isOpen, setIsOpen] = useState(false)
  const groupName = new Map((groups.data ?? []).map((g) => [g.id, g.name]))

  const remove = async (p: Plan) => {
    const err = await mutate(() => api.DELETE('/api/admin/plans/{id}', { params: { ...csrfHeader(), path: { id: p.id } } }))
    if (err !== null) {
      void message.error(err.message)
      return
    }
    void message.success('已删除')
    plans.reload()
  }

  const columns: TableProps<Plan>['columns'] = [
    { title: '名称', dataIndex: 'name' },
    { title: '流量', dataIndex: 'trafficLimit', render: (v: number) => (v === 0 ? '不限' : formatBytes(v)) },
    { title: '时长', key: 'duration', render: (_: unknown, p: Plan) => (p.durationDays === 0 ? '不限' : `${p.durationDays} 天${p.startOnFirstUse ? '（首次使用起算）' : ''}`) },
    { title: '节点组', dataIndex: 'nodeGroupIds', render: (ids: number[]) => (ids.length === 0 ? '—' : ids.map((id) => <Tag key={id}>{groupName.get(id) ?? id}</Tag>)) },
    { title: 'IP 限制', dataIndex: 'limitIp', render: (v: number) => (v === 0 ? '不限' : v) },
    {
      title: '售价',
      key: 'prices',
      render: (_: unknown, p: Plan) =>
        p.prices.length === 0 ? '未定价' : p.prices.map((price) => <Tag key={price.id}>{`${price.durationDays} 天 ${formatMoney(price.price, currency)}`}</Tag>),
    },
    {
      title: '商店',
      key: 'sale',
      render: (_: unknown, p: Plan) => (
        <Space size={4}>
          {p.isOnSale === true ? <Tag color="green">上架</Tag> : <Tag>未上架</Tag>}
          {p.isTrial === true && <Tag color="gold">试用</Tag>}
        </Space>
      ),
    },
    { title: '流量重置', key: 'reset', render: (_: unknown, p: Plan) => describeReset(p.trafficReset ?? 'never', p.trafficResetDay ?? 1) },
    { title: '用户数', dataIndex: 'userCount' },
  ]
  if (canWrite) {
    columns.push({
      title: '操作',
      key: 'actions',
      render: (_: unknown, p: Plan) => (
        <Space>
          <Button
            size="small"
            onClick={() => {
              setEditing(p)
              setIsOpen(true)
            }}
          >
            编辑
          </Button>
          <Popconfirm title={`删除套餐 ${p.name}？`} description="已分配的用户保留额度与到期，但失去节点授权" onConfirm={() => remove(p)}>
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
      <Space className="page-header">
        <Typography.Title level={3}>套餐</Typography.Title>
        {canWrite && (
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => {
              setEditing(null)
              setIsOpen(true)
            }}
          >
            新建套餐
          </Button>
        )}
      </Space>
      {plans.error !== null && <Alert type="error" title={plans.error.message} showIcon className="page-alert" />}
      <Table<Plan> rowKey="id" scroll={TABLE_SCROLL} loading={plans.isLoading} columns={columns} dataSource={plans.data ?? []} pagination={false} />
      <PlanModal
        plan={editing}
        isOpen={isOpen}
        groups={groups.data ?? []}
        onClose={() => setIsOpen(false)}
        onSaved={() => {
          setIsOpen(false)
          plans.reload()
        }}
      />
    </>
  )
}

interface PlanModalProps {
  plan: Plan | null
  isOpen: boolean
  groups: NodeGroup[]
  onClose: () => void
  onSaved: () => void
}

function PlanModal({ plan, isOpen, groups, onClose, onSaved }: PlanModalProps) {
  const generation = useOpenGeneration(isOpen)
  return (
    <Modal title={plan === null ? '新建套餐' : `编辑套餐：${plan.name}`} open={isOpen} onCancel={onClose} footer={null} destroyOnHidden width={MODAL_WIDTH}>
      <PlanFormBody key={generation} plan={plan} groups={groups} onSaved={onSaved} />
    </Modal>
  )
}

function PlanFormBody({ plan, groups, onSaved }: { plan: Plan | null; groups: NodeGroup[]; onSaved: () => void }) {
  const { message } = App.useApp()
  const [form] = Form.useForm<PlanForm>()
  const [error, setError] = useState<ApiError | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const submit = async (v: PlanForm) => {
    setIsBusy(true)
    setError(null)
    const body = inputOf(v)
    const err = await mutate(() =>
      plan === null
        ? api.POST('/api/admin/plans', { params: csrfHeader(), body })
        : api.PUT('/api/admin/plans/{id}', { params: { ...csrfHeader(), path: { id: plan.id } }, body }),
    )
    setIsBusy(false)
    if (err !== null) {
      setError(err)
      return
    }
    void message.success(plan === null ? '已创建套餐' : '已保存（不影响已分配的用户）')
    onSaved()
  }

  return (
    <>
      {error !== null && <Alert type="error" title={error.message} showIcon className="page-alert" />}
      <Form<PlanForm> form={form} layout="vertical" initialValues={formOf(plan)} onFinish={submit}>
        <Form.Item name="name" label="名称" rules={[{ required: true, max: MAX_NAME_LEN, message: `1–${MAX_NAME_LEN} 个字符` }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="trafficGb" label="流量额度（GB）" extra="0 表示不限；上行 + 下行合计">
          <InputNumber min={0} className="full-width" />
        </Form.Item>
        <Form.Item name="durationDays" label="时长（天）" extra="0 表示不限">
          <InputNumber min={0} max={MAX_DURATION_DAYS} className="full-width" />
        </Form.Item>
        <Form.Item name="startOnFirstUse" label="首次使用后才开始计时" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name="nodeGroupIds" label="节点组">
          <Select mode="multiple" optionFilterProp="label" options={groups.map((g) => ({ value: g.id, label: g.name }))} />
        </Form.Item>
        <Form.Item name="limitIp" label="同时在线 IP 数" extra="0 表示不限；由各面板执行">
          <InputNumber min={0} max={MAX_LIMIT_IP} className="full-width" />
        </Form.Item>
        <ResetFields />
        <SaleFields />
        <Form.Item name="allowClientChain" label="允许客户端链式代理（M6 起生效）" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name="allowCustomNode" label="允许导入自有落地节点（M6 起生效）" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name="remark" label="备注">
          <Input autoComplete="off" />
        </Form.Item>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          {plan === null ? '创建' : '保存'}
        </Button>
      </Form>
    </>
  )
}

/** 售卖属性与时长选项（docs/03 §5.12）：没有时长选项或未上架的套餐不出现在商店，但仍可手动分配。须放在 antd Form 内。 */
function SaleFields() {
  return (
    <>
      <Form.Item name="isOnSale" label="在商店上架" extra="还需要至少一个时长选项才会显示" valuePropName="checked">
        <Switch />
      </Form.Item>
      <Form.Item label="时长选项" extra={`最多 ${MAX_PRICES} 个，天数不能重复；流量是否按月重置取上面的“周期流量重置”`}>
        <Form.List name="prices">
          {(fields, { add, remove }) => (
            <Space orientation="vertical" className="full-width">
              {fields.map((field) => (
                <Space key={field.key} align="baseline">
                  <Form.Item name={[field.name, 'durationDays']} noStyle rules={[{ required: true, type: 'integer', min: 1, max: MAX_PRICE_DAYS, message: '请填天数' }]}>
                    <InputNumber min={1} max={MAX_PRICE_DAYS} precision={0} placeholder="天数" aria-label="天数" suffix="天" />
                  </Form.Item>
                  <Form.Item name={[field.name, 'amount']} noStyle rules={[{ required: true, type: 'number', min: 0, max: MAX_MONEY_AMOUNT, message: '请填价格' }]}>
                    <InputNumber min={0} max={MAX_MONEY_AMOUNT} precision={MONEY_PRECISION} placeholder="价格" aria-label="价格" />
                  </Form.Item>
                  <Button type="text" icon={<MinusCircleOutlined />} aria-label="移除时长选项" onClick={() => remove(field.name)} />
                </Space>
              ))}
              {fields.length < MAX_PRICES && (
                <Button type="dashed" icon={<PlusOutlined />} onClick={() => add({ durationDays: undefined, amount: undefined })}>
                  添加时长选项
                </Button>
              )}
            </Space>
          )}
        </Form.List>
      </Form.Item>
      <Form.Item name="isTrial" label="试用套餐" extra="每个用户终身只能下单一次，不能用优惠码，不能自动续费" valuePropName="checked">
        <Switch />
      </Form.Item>
      <Form.Item name="description" label="卖点说明" extra="纯文本，一行一条，显示在商店的价格卡上" rules={[{ max: MAX_DESCRIPTION_LEN }]}>
        <Input.TextArea rows={DESCRIPTION_ROWS} maxLength={MAX_DESCRIPTION_LEN} showCount />
      </Form.Item>
      <Form.Item name="sortOrder" label="商店排序" extra="小的在前">
        <InputNumber min={0} max={MAX_SORT_ORDER} precision={0} className="full-width" />
      </Form.Item>
    </>
  )
}
