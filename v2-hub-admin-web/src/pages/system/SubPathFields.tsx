import { Button, Form, Input, InputNumber, Space, Typography } from 'antd'
import type { Settings } from '../../api/client'
import { formatTime } from './format'

// 取值范围与 hub-api.yaml 的 Settings.subscription 一致（docs/03 §5.4 订阅路径，Ruling-59）
const SUB_PATH_PATTERN = /^[a-z0-9]{10,32}$/
const SUB_PATH_LEN = 16
const SUB_PATH_ALPHABET = 'abcdefghijklmnopqrstuvwxyz0123456789'
const MAX_PATH_GRACE_DAYS = 90
const PATH_FIELD: [string, string] = ['subscription', 'path']

/** 浏览器里生成随机前缀：16 位小写字母与数字（用 crypto.getRandomValues，不用 Math.random）。 */
function randomSubPath(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(SUB_PATH_LEN))
  return Array.from(bytes, (b) => SUB_PATH_ALPHABET[b % SUB_PATH_ALPHABET.length]).join('')
}

interface SubPathFieldsProps {
  subscription: Settings['subscription']
  canWrite: boolean
}

/** 系统设置“订阅”卡片里的路径前缀：可改写或随机生成，旧前缀按过渡天数保留（只读显示）。 */
export function SubPathFields({ subscription, canWrite }: SubPathFieldsProps) {
  const form = Form.useFormInstance()
  const hasOldPath = subscription.oldPath !== '' && subscription.oldPathUntil !== null
  return (
    <>
      <Form.Item label="订阅路径前缀" required extra="订阅地址形如 https://域名/前缀/令牌；保存后旧前缀按下面的天数继续可用，到期失效">
        <Space.Compact className="full-width">
          <Form.Item
            name={PATH_FIELD}
            noStyle
            rules={[{ required: true, pattern: SUB_PATH_PATTERN, message: '10–32 位小写字母或数字' }]}
          >
            <Input autoComplete="off" aria-label="订阅路径前缀" className="mono" />
          </Form.Item>
          {canWrite && <Button onClick={() => form.setFieldValue(PATH_FIELD, randomSubPath())}>随机生成</Button>}
        </Space.Compact>
      </Form.Item>
      <Form.Item name={['subscription', 'pathGraceDays']} label="改前缀后旧前缀还能用几天" extra="0 = 立即失效；只保留最近一个旧前缀" rules={[{ required: true, message: '必填' }]}>
        <InputNumber min={0} max={MAX_PATH_GRACE_DAYS} precision={0} className="full-width" />
      </Form.Item>
      <Typography.Paragraph type="secondary">
        {hasOldPath ? (
          <>
            过渡中的旧前缀：<Typography.Text code>{subscription.oldPath}</Typography.Text>，{formatTime(subscription.oldPathUntil)} 失效
          </>
        ) : (
          '没有过渡中的旧前缀'
        )}
      </Typography.Paragraph>
    </>
  )
}
