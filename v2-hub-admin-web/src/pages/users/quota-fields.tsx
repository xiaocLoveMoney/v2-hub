import { Form, InputNumber, Radio, Select, Tag } from 'antd'
import type { TrafficReset, UserStatus } from '../../api/client'
import { TRAFFIC_RESET_OPTIONS, USER_STATUS, WEEKDAY_OPTIONS, type RenewMode } from './quota'

export function UserStatusTag({ status }: { status: UserStatus }) {
  const s = USER_STATUS[status]
  return <Tag color={s.color}>{s.label}</Tag>
}

const MAX_MONTH_DAY = 31
const MAX_RENEW_DAYS = 3650
const MAX_RENEW_TIMES = 10000

/** 表单里续期与周期重置的字段名（plan 与 quota 两个表单共用）。 */
export interface RenewResetForm {
  renewMode: RenewMode
  renewValue: number
  renewMax: number
  trafficReset: TrafficReset
  trafficResetDay: number
}

/** 自动续期与周期流量重置的表单片段（套餐表单与改用量表单共用，须放在 antd Form 内）。 */
export function RenewResetFields() {
  const mode = Form.useWatch<RenewMode>('renewMode')
  const reset = Form.useWatch<TrafficReset>('trafficReset')
  return (
    <>
      <Form.Item name="renewMode" label="到期自动续期" extra="到期后按规则顺延并清零已用流量（照 3x-ui）">
        <Radio.Group
          optionType="button"
          options={[
            { value: 'none', label: '不续期' },
            { value: 'days', label: '每 N 天' },
            { value: 'month', label: '每月几号' },
            { value: 'week', label: '每周几' },
          ]}
        />
      </Form.Item>
      {mode === 'days' && (
        <Form.Item name="renewValue" label="续期天数" rules={[{ required: true, type: 'integer', min: 1, max: MAX_RENEW_DAYS }]}>
          <InputNumber min={1} max={MAX_RENEW_DAYS} className="full-width" />
        </Form.Item>
      )}
      {mode === 'month' && (
        <Form.Item name="renewValue" label="每月几号" rules={[{ required: true, type: 'integer', min: 1, max: MAX_MONTH_DAY }]}>
          <InputNumber min={1} max={MAX_MONTH_DAY} className="full-width" />
        </Form.Item>
      )}
      {mode === 'week' && (
        <Form.Item name="renewValue" label="每周几" rules={[{ required: true }]}>
          <Select options={WEEKDAY_OPTIONS} />
        </Form.Item>
      )}
      {mode !== undefined && mode !== 'none' && (
        <Form.Item name="renewMax" label="最多续期次数" extra="0 表示不限">
          <InputNumber min={0} max={MAX_RENEW_TIMES} className="full-width" />
        </Form.Item>
      )}
      <Form.Item name="trafficReset" label="周期流量重置" extra="到点清零已用流量，到期时间不变">
        <Select options={TRAFFIC_RESET_OPTIONS} />
      </Form.Item>
      {reset === 'monthly' && (
        <Form.Item name="trafficResetDay" label="每月几号重置" rules={[{ required: true, type: 'integer', min: 1, max: MAX_MONTH_DAY }]}>
          <InputNumber min={1} max={MAX_MONTH_DAY} className="full-width" />
        </Form.Item>
      )}
    </>
  )
}
