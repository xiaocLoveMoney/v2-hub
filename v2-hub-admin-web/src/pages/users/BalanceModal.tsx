import { Button, Form, Input, InputNumber, Modal, Typography } from 'antd'
import { api, csrfHeader, type EndUser } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useOpenGeneration } from '../../hooks/use-open-generation'
import { useSubmit } from '../../hooks/use-submit'
import { MAX_MONEY_AMOUNT, MONEY_PRECISION, amountToCents, formatMoney } from '../../money'

const MAX_REMARK_LEN = 255

interface BalanceForm {
  /** 带符号的调整金额（元）：正数加、负数减。 */
  amount: number
  remark: string
}

interface BalanceModalProps {
  /** null = 关闭。 */
  user: EndUser | null
  currency: string
  onClose: () => void
  onSaved: () => void
}

/** 调整用户余额（finance:write，docs/03 §5.14）：可正可负、须写备注；退款、补偿、纠错都走这里，每次一条流水与审计。 */
export function BalanceModal({ user, currency, onClose, onSaved }: BalanceModalProps) {
  const generation = useOpenGeneration(user !== null)
  return (
    <Modal title={`调整余额：${user?.username ?? ''}`} open={user !== null} onCancel={onClose} footer={null} destroyOnHidden>
      {user !== null && <BalanceFormBody key={generation} user={user} currency={currency} onSaved={onSaved} />}
    </Modal>
  )
}

function BalanceFormBody({ user, currency, onSaved }: { user: EndUser; currency: string; onSaved: () => void }) {
  const { error, isBusy, run } = useSubmit('余额已调整', onSaved)
  const submit = (v: BalanceForm) =>
    run(() =>
      api.POST('/api/admin/users/{id}/balance', { params: { ...csrfHeader(), path: { id: user.id } }, body: { amount: amountToCents(v.amount), remark: v.remark.trim() } }),
    )
  return (
    <>
      <ErrorAlert error={error} />
      <Typography.Paragraph>当前余额：{formatMoney(user.balance ?? 0, currency)}</Typography.Paragraph>
      <Form<BalanceForm> layout="vertical" onFinish={(v) => void submit(v)}>
        <Form.Item
          name="amount"
          label="调整金额"
          extra="正数增加、负数扣减；调整后余额不能为负"
          rules={[
            { required: true, message: '请填金额' },
            { validator: async (_: unknown, v: number | null) => (v === 0 ? Promise.reject(new Error('金额不能为 0')) : undefined) },
          ]}
        >
          <InputNumber min={-MAX_MONEY_AMOUNT} max={MAX_MONEY_AMOUNT} precision={MONEY_PRECISION} className="full-width" />
        </Form.Item>
        <Form.Item name="remark" label="备注" extra="会写进余额流水，用户能看到" rules={[{ required: true, whitespace: true, max: MAX_REMARK_LEN, message: `请填备注（1–${MAX_REMARK_LEN} 个字符）` }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          确认调整
        </Button>
      </Form>
    </>
  )
}
