import { PlusOutlined } from '@ant-design/icons'
import { Button, Form, Input, InputNumber, Modal, Popconfirm, Space, Switch, Table, Tag, Typography, type TableProps } from 'antd'
import { useState } from 'react'
import { api, csrfHeader, type TrafficPack, type TrafficPackInput } from '../../api/client'
import { useSession } from '../../auth/session-context'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useCurrency } from '../../hooks/use-currency'
import { useOpenGeneration } from '../../hooks/use-open-generation'
import { useRemote } from '../../hooks/use-remote'
import { useRowAction, useSubmit } from '../../hooks/use-submit'
import { PERMISSIONS } from '../../menu'
import { MAX_MONEY_AMOUNT, MONEY_PRECISION, amountToCents, centsToAmount, formatMoney } from '../../money'
import { TABLE_SCROLL } from '../system/format'
import { bytesToGb, formatBytes, gbToBytes } from '../users/quota'

const MAX_NAME_LEN = 64
const MAX_SORT_ORDER = 100000
const MIN_TRAFFIC_GB = 0.01
const TRAFFIC_PRECISION = 2

/** 表单值：流量按 GB、价格按元输入，提交时换成字节与分。 */
interface PackForm {
  name: string
  trafficGb: number
  amount: number
  isOnSale: boolean
  sortOrder: number
}

const loadPacks = (signal: AbortSignal) => api.GET('/api/admin/traffic-packs', { signal })

function formOf(p: TrafficPack | null): PackForm {
  if (p === null) {
    return { name: '', trafficGb: 10, amount: 0, isOnSale: true, sortOrder: 0 }
  }
  return { name: p.name, trafficGb: bytesToGb(p.trafficBytes), amount: centsToAmount(p.price), isOnSale: p.isOnSale, sortOrder: p.sortOrder ?? 0 }
}

function inputOf(v: PackForm): TrafficPackInput {
  return { name: v.name, trafficBytes: gbToBytes(v.trafficGb), price: amountToCents(v.amount), isOnSale: v.isOnSale, sortOrder: v.sortOrder ?? 0 }
}

/** 流量包（shop:read 查看，shop:write 增改删）：只加流量额度、不改到期（docs/03 §5.12）。 */
export function TrafficPacksPage() {
  const { can } = useSession()
  const canWrite = can(PERMISSIONS.SHOP_WRITE)
  const currency = useCurrency()
  const packs = useRemote<TrafficPack[]>(loadPacks)
  const act = useRowAction(packs.reload)
  const [editing, setEditing] = useState<TrafficPack | null>(null)
  const [isOpen, setIsOpen] = useState(false)

  const open = (p: TrafficPack | null) => {
    setEditing(p)
    setIsOpen(true)
  }

  const columns: TableProps<TrafficPack>['columns'] = [
    { title: '名称', dataIndex: 'name' },
    { title: '流量', dataIndex: 'trafficBytes', render: (v: number) => formatBytes(v) },
    { title: '价格', dataIndex: 'price', render: (v: number) => formatMoney(v, currency) },
    { title: '排序', dataIndex: 'sortOrder' },
    { title: '状态', dataIndex: 'isOnSale', render: (isOnSale: boolean) => (isOnSale ? <Tag color="green">上架</Tag> : <Tag>未上架</Tag>) },
  ]
  if (canWrite) {
    columns.push({
      title: '操作',
      key: 'actions',
      render: (_: unknown, p: TrafficPack) => (
        <Space>
          <Button size="small" onClick={() => open(p)}>
            编辑
          </Button>
          <Popconfirm
            title={`删除流量包 ${p.name}？`}
            description="已有的订单保留下单时的快照"
            onConfirm={() => act(() => api.DELETE('/api/admin/traffic-packs/{id}', { params: { ...csrfHeader(), path: { id: p.id } } }), '已删除')}
          >
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
        <Typography.Title level={3}>流量包</Typography.Title>
        {canWrite && (
          <Button type="primary" icon={<PlusOutlined />} onClick={() => open(null)}>
            新建流量包
          </Button>
        )}
      </Space>
      <ErrorAlert error={packs.error} />
      <Table<TrafficPack> rowKey="id" scroll={TABLE_SCROLL} loading={packs.isLoading} columns={columns} dataSource={packs.data ?? []} pagination={false} />
      <PackModal
        pack={editing}
        isOpen={isOpen}
        onClose={() => setIsOpen(false)}
        onSaved={() => {
          setIsOpen(false)
          packs.reload()
        }}
      />
    </>
  )
}

interface PackModalProps {
  /** null = 新建。 */
  pack: TrafficPack | null
  isOpen: boolean
  onClose: () => void
  onSaved: () => void
}

function PackModal({ pack, isOpen, onClose, onSaved }: PackModalProps) {
  const generation = useOpenGeneration(isOpen)
  return (
    <Modal title={pack === null ? '新建流量包' : `编辑流量包：${pack.name}`} open={isOpen} onCancel={onClose} footer={null} destroyOnHidden>
      <PackFormBody key={generation} pack={pack} onSaved={onSaved} />
    </Modal>
  )
}

function PackFormBody({ pack, onSaved }: Pick<PackModalProps, 'pack' | 'onSaved'>) {
  const { error, isBusy, run } = useSubmit(pack === null ? '已创建流量包' : '已保存', onSaved)
  const submit = (v: PackForm) => {
    const body = inputOf(v)
    return run(() =>
      pack === null
        ? api.POST('/api/admin/traffic-packs', { params: csrfHeader(), body })
        : api.PUT('/api/admin/traffic-packs/{id}', { params: { ...csrfHeader(), path: { id: pack.id } }, body }),
    )
  }
  return (
    <>
      <ErrorAlert error={error} />
      <Form<PackForm> layout="vertical" initialValues={formOf(pack)} onFinish={(v) => void submit(v)}>
        <Form.Item name="name" label="名称" rules={[{ required: true, max: MAX_NAME_LEN, message: `1–${MAX_NAME_LEN} 个字符` }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="trafficGb" label="流量（GB）" extra="加到用户当前的流量额度上，续费或换购后不保留" rules={[{ required: true, message: '请填流量' }]}>
          <InputNumber min={MIN_TRAFFIC_GB} precision={TRAFFIC_PRECISION} className="full-width" />
        </Form.Item>
        <Form.Item name="amount" label="价格" rules={[{ required: true, message: '请填价格' }]}>
          <InputNumber min={0} max={MAX_MONEY_AMOUNT} precision={MONEY_PRECISION} className="full-width" />
        </Form.Item>
        <Form.Item name="sortOrder" label="排序" extra="小的在前">
          <InputNumber min={0} max={MAX_SORT_ORDER} precision={0} className="full-width" />
        </Form.Item>
        <Form.Item name="isOnSale" label="在商店上架" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          {pack === null ? '创建' : '保存'}
        </Button>
      </Form>
    </>
  )
}
