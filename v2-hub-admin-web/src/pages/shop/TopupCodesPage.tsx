import { PlusOutlined } from '@ant-design/icons'
import { Alert, App, Button, DatePicker, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Tag, Typography, type TableProps } from 'antd'
import type { Dayjs } from 'dayjs'
import { useCallback, useState } from 'react'
import { api, csrfHeader, toApiError, type TopupCode, type TopupCodeStatus } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { ErrorAlert } from '../../components/ErrorAlert'
import { PagedTable } from '../../components/PagedTable'
import { useCurrency } from '../../hooks/use-currency'
import { useOpenGeneration } from '../../hooks/use-open-generation'
import { useRowAction, useSubmit } from '../../hooks/use-submit'
import { PERMISSIONS } from '../../menu'
import { MAX_MONEY_AMOUNT, MONEY_PRECISION, amountToCents, formatMoney } from '../../money'
import { formatTime } from '../system/format'

const MAX_BATCH_COUNT = 500
const MAX_BATCH_LEN = 64
const MIN_AMOUNT = 0.01
const CODE_GROUP_SIZE = 4
const CODES_ROWS = 10
const CODE_GROUP_PATTERN = new RegExp(`.{1,${CODE_GROUP_SIZE}}`, 'g')

/** 充值码状态的显示名与颜色；取值见 hub-api.yaml 的 TopupCodeStatus。 */
const STATUS_META: Record<TopupCodeStatus, { label: string; color?: string }> = {
  unused: { label: '未使用', color: 'green' },
  used: { label: '已使用', color: 'blue' },
  disabled: { label: '已停用', color: 'red' },
  expired: { label: '已过期' },
}
const STATUS_OPTIONS = (Object.keys(STATUS_META) as TopupCodeStatus[]).map((value) => ({ value, label: STATUS_META[value].label }))

/** 每 4 位一组显示，便于核对与口述（docs/03 §5.15）。 */
function groupCode(code: string): string {
  return (code.match(CODE_GROUP_PATTERN) ?? [code]).join(' ')
}

interface BatchForm {
  amount: number
  count: number
  expiresAt?: Dayjs | null
  batch: string
}

/** 一批刚生成的充值码：明文只在这次响应里出现。 */
interface GeneratedBatch {
  amount: number
  codes: string[]
}

/** 充值码（finance:read 查看，finance:write 生成 / 停用 / 删除 / 查看明文）：码等同现金，列表只显示末 4 位（Ruling-31）。 */
export function TopupCodesPage() {
  const { can } = useSession()
  const canWrite = can(PERMISSIONS.FINANCE_WRITE)
  const currency = useCurrency()
  const [status, setStatus] = useState<TopupCodeStatus | undefined>(undefined)
  const [batch, setBatch] = useState('')
  const [refreshKey, setRefreshKey] = useState(0)
  const reload = () => setRefreshKey((k) => k + 1)
  const act = useRowAction(reload)
  const [isCreating, setIsCreating] = useState(false)
  const [generated, setGenerated] = useState<GeneratedBatch | null>(null)
  const load = useCallback(
    (page: number, pageSize: number, signal: AbortSignal) =>
      api.GET('/api/admin/topup-codes', { params: { query: { page, pageSize, status, batch: batch === '' ? undefined : batch } }, signal }),
    [status, batch],
  )

  const columns: TableProps<TopupCode>['columns'] = [
    { title: '充值码', dataIndex: 'codeTail', render: (tail: string) => <span className="mono">···· {tail}</span> },
    { title: '面值', dataIndex: 'amount', render: (v: number) => formatMoney(v, currency) },
    { title: '批次', dataIndex: 'batch', render: (v: string) => v || '—' },
    { title: '状态', dataIndex: 'status', render: (s: TopupCodeStatus) => <Tag color={STATUS_META[s]?.color}>{STATUS_META[s]?.label ?? s}</Tag> },
    { title: '到期', dataIndex: 'expiresAt', render: (v: string | null) => (v === null ? '不过期' : formatTime(v)) },
    { title: '使用人', dataIndex: 'usedByUsername', render: (v: string) => v || '—' },
    { title: '使用时间', dataIndex: 'usedAt', render: (v: string | null) => formatTime(v) },
    { title: '生成时间', dataIndex: 'createdAt', render: (v: string) => formatTime(v) },
  ]
  if (canWrite) {
    columns.push({
      title: '操作',
      key: 'actions',
      render: (_: unknown, c: TopupCode) => (
        <Space>
          <RevealButton codeId={c.id} />
          {c.usedAt === null && (
            <>
              <Button
                size="small"
                onClick={() =>
                  void act(
                    () => api.PUT('/api/admin/topup-codes/{id}', { params: { ...csrfHeader(), path: { id: c.id } }, body: { isDisabled: !c.isDisabled } }),
                    c.isDisabled ? '已启用' : '已停用',
                  )
                }
              >
                {c.isDisabled ? '启用' : '停用'}
              </Button>
              <Popconfirm
                title="删除这个充值码？"
                onConfirm={() => act(() => api.DELETE('/api/admin/topup-codes/{id}', { params: { ...csrfHeader(), path: { id: c.id } } }), '已删除')}
              >
                <Button size="small" danger>
                  删除
                </Button>
              </Popconfirm>
            </>
          )}
        </Space>
      ),
    })
  }

  return (
    <>
      <Space className="page-header">
        <Typography.Title level={3}>充值码</Typography.Title>
        {canWrite && (
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setIsCreating(true)}>
            生成充值码
          </Button>
        )}
      </Space>
      <PagedTable<TopupCode>
        load={load}
        columns={columns}
        refreshKey={refreshKey}
        filters={
          <>
            <Select
              allowClear
              placeholder="按状态过滤"
              aria-label="按状态过滤"
              className="filter-select"
              options={STATUS_OPTIONS}
              value={status}
              onChange={(v: TopupCodeStatus | undefined) => setStatus(v)}
            />
            <Input.Search allowClear placeholder="按批次备注过滤" aria-label="按批次备注过滤" className="filter-select" maxLength={MAX_BATCH_LEN} onSearch={(v) => setBatch(v.trim())} />
          </>
        }
      />
      <BatchModal
        isOpen={isCreating}
        onClose={() => setIsCreating(false)}
        onGenerated={(result) => {
          setIsCreating(false)
          setGenerated(result)
          reload()
        }}
      />
      <GeneratedModal batch={generated} currency={currency} onClose={() => setGenerated(null)} />
    </>
  )
}

interface BatchModalProps {
  isOpen: boolean
  onClose: () => void
  onGenerated: (batch: GeneratedBatch) => void
}

function BatchModal({ isOpen, onClose, onGenerated }: BatchModalProps) {
  const generation = useOpenGeneration(isOpen)
  return (
    <Modal title="生成充值码" open={isOpen} onCancel={onClose} footer={null} destroyOnHidden>
      <BatchFormBody key={generation} onGenerated={onGenerated} />
    </Modal>
  )
}

function BatchFormBody({ onGenerated }: Pick<BatchModalProps, 'onGenerated'>) {
  const { error, isBusy, run } = useSubmit('已生成', () => undefined)
  const submit = async (v: BatchForm) => {
    // 明文只在这次响应里：用一个盒子把结果从请求闭包里带出来，成功后交给上层展示
    const out: { batch: GeneratedBatch | null } = { batch: null }
    await run(async () => {
      const res = await api.POST('/api/admin/topup-codes', {
        params: csrfHeader(),
        body: { amount: amountToCents(v.amount), count: v.count, expiresAt: v.expiresAt ? v.expiresAt.toISOString() : null, batch: v.batch ?? '' },
      })
      out.batch = res.data?.data ?? null
      return res
    })
    if (out.batch !== null) {
      onGenerated(out.batch)
    }
  }
  return (
    <>
      <ErrorAlert error={error} />
      <Form<BatchForm> layout="vertical" initialValues={{ amount: 10, count: 10, expiresAt: null, batch: '' }} onFinish={(v) => void submit(v)}>
        <Form.Item name="amount" label="面值" rules={[{ required: true, message: '请填面值' }]}>
          <InputNumber min={MIN_AMOUNT} max={MAX_MONEY_AMOUNT} precision={MONEY_PRECISION} className="full-width" />
        </Form.Item>
        <Form.Item name="count" label="数量" extra={`一次最多 ${MAX_BATCH_COUNT} 个`} rules={[{ required: true, message: '请填数量' }]}>
          <InputNumber min={1} max={MAX_BATCH_COUNT} precision={0} className="full-width" />
        </Form.Item>
        <Form.Item name="expiresAt" label="到期时间" extra="留空 = 不过期">
          <DatePicker showTime allowClear className="full-width" />
        </Form.Item>
        <Form.Item name="batch" label="批次备注" extra="方便之后按批次查找" rules={[{ max: MAX_BATCH_LEN }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          生成
        </Button>
      </Form>
    </>
  )
}

interface GeneratedModalProps {
  batch: GeneratedBatch | null
  currency: string
  onClose: () => void
}

/** 展示刚生成的一批明文：关掉之后就只能逐条“查看”了，所以提供整批复制。 */
function GeneratedModal({ batch, currency, onClose }: GeneratedModalProps) {
  const { message } = App.useApp()
  const text = (batch?.codes ?? []).map(groupCode).join('\n')
  const copyAll = async () => {
    try {
      await navigator.clipboard.writeText(text)
      void message.success('已复制整批充值码')
    } catch (err) {
      reportError(err)
      void message.error('复制失败，请手动选中复制')
    }
  }
  return (
    <Modal
      title={batch === null ? '' : `已生成 ${batch.codes.length} 个充值码（面值 ${formatMoney(batch.amount, currency)}）`}
      open={batch !== null}
      onCancel={onClose}
      destroyOnHidden
      footer={
        <Space>
          <Button onClick={() => void copyAll()}>复制全部</Button>
          <Button type="primary" onClick={onClose}>
            我已保存
          </Button>
        </Space>
      }
    >
      <Alert type="warning" showIcon className="page-alert" title="充值码等同现金。关闭后这里不再显示整批明文，列表里只有末 4 位。" />
      <Input.TextArea readOnly rows={CODES_ROWS} className="mono" value={text} aria-label="生成的充值码" />
    </Modal>
  )
}

/** 单条查看明文：点了才向后端取（记审计），只留在弹窗里，不进列表状态。 */
function RevealButton({ codeId }: { codeId: number }) {
  const { message, modal } = App.useApp()
  const [isBusy, setIsBusy] = useState(false)
  const reveal = async () => {
    setIsBusy(true)
    try {
      const res = await api.GET('/api/admin/topup-codes/{id}/code', { params: { path: { id: codeId } } })
      if (res.error !== undefined || res.data?.data === undefined) {
        void message.error(toApiError(res.error).message)
        return
      }
      modal.info({
        title: '充值码',
        content: (
          <Typography.Text copyable={{ text: res.data.data.code }} className="mono">
            {groupCode(res.data.data.code)}
          </Typography.Text>
        ),
      })
    } catch (err) {
      reportError(err)
      void message.error(toApiError(err).message)
    } finally {
      setIsBusy(false)
    }
  }
  return (
    <Button size="small" loading={isBusy} onClick={() => void reveal()}>
      查看
    </Button>
  )
}
