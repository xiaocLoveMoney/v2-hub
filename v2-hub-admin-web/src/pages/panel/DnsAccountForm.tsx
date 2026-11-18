import { Button, Form, Input, Select } from 'antd'
import { api, csrfHeader, type DnsAccount, type DnsAccountInput, type DnsProviderType } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useSubmit } from '../../hooks/use-submit'
import { MAX_NAME_LEN, MAX_REMARK_LEN } from '../form-limits'
import { DNS_PROVIDER_LABELS } from './host-labels'

const PROVIDER_OPTIONS = (Object.keys(DNS_PROVIDER_LABELS) as DnsProviderType[]).map((value) => ({ value, label: DNS_PROVIDER_LABELS[value] }))
const DEFAULT_PROVIDER: DnsProviderType = 'cloudflare'
/** 根域名与令牌的最大长度（hub-api.yaml DnsAccountInput）。 */
const MAX_ZONE_LEN = 255
const MAX_TOKEN_LEN = 255
/** 根域名的写法：至少两段，每段字母数字开头结尾。 */
const ZONE_PATTERN = /^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$/
const KEEP_PLACEHOLDER = '留空 = 沿用原值；已保存的令牌不会显示'

interface AccountForm {
  name: string
  providerType: DnsProviderType
  zone: string
  token?: string
  remark?: string
}

/** 表单值转请求体。令牌只写：留空就不带这个字段，表示沿用原值（hub-api.yaml DnsAccountInput）。 */
function toInput(v: AccountForm): DnsAccountInput {
  const input: DnsAccountInput = { name: v.name.trim(), providerType: v.providerType, zone: v.zone.trim(), remark: v.remark ?? '' }
  const token = (v.token ?? '').trim()
  if (token !== '') {
    input.token = token
  }
  return input
}

interface DnsAccountFormProps {
  /** null 表示新建。 */
  account: DnsAccount | null
  /** 保存成功后调用（关闭表单、刷新列表）。 */
  onSaved: () => void
}

/**
 * 新建 / 编辑 DNS 账号的表单（host:write）。令牌只写不读：接口不返回，表单也不回显，编辑时留空表示沿用原值；
 * 保存时后端用令牌在线查一次根域名，查不到或被拒会报错（docs/03 §5.22 DNS 账号）。
 */
export function DnsAccountForm({ account, onSaved }: DnsAccountFormProps) {
  const [form] = Form.useForm<AccountForm>()
  const isEdit = account !== null
  const submit = useSubmit(isEdit ? '已保存' : '已新建', onSaved)
  const isInUse = isEdit && account.hostCount > 0

  const handleFinish = (v: AccountForm) => {
    const body = toInput(v)
    return submit.run(() =>
      isEdit
        ? api.PUT('/api/admin/dns-accounts/{id}', { params: { ...csrfHeader(), path: { id: account.id } }, body })
        : api.POST('/api/admin/dns-accounts', { params: csrfHeader(), body }),
    )
  }

  return (
    <>
      <ErrorAlert error={submit.error} />
      <Form<AccountForm>
        form={form}
        layout="vertical"
        onFinish={handleFinish}
        initialValues={{
          name: account?.name ?? '',
          providerType: account?.providerType ?? DEFAULT_PROVIDER,
          zone: account?.zone ?? '',
          remark: account?.remark ?? '',
        }}
      >
        <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true, max: MAX_NAME_LEN, message: `1–${MAX_NAME_LEN} 个字符` }]}>
          <Input autoComplete="off" placeholder="如 cf-example-com" />
        </Form.Item>
        <Form.Item name="providerType" label="服务商">
          <Select options={PROVIDER_OPTIONS} disabled={isInUse} />
        </Form.Item>
        <Form.Item
          name="zone"
          label="根域名"
          extra={isInUse ? '还有主机在用这个账号，不能改根域名' : '在服务商那里托管的域名，如 example.com；主机的域名都在它之下'}
          rules={[{ required: true, max: MAX_ZONE_LEN, pattern: ZONE_PATTERN, message: '请输入根域名，如 example.com' }]}
        >
          <Input autoComplete="off" disabled={isInUse} placeholder="example.com" />
        </Form.Item>
        <Form.Item
          name="token"
          label="API 令牌"
          extra="Cloudflare 后台用“编辑区域 DNS（Edit zone DNS）”模板创建，区域只选这个域名。保存后加密存储，不再显示；只在申请证书时临时传给对应主机、用完即删，不在主机上保存"
          rules={[{ required: !isEdit, message: '请输入 API 令牌' }]}
        >
          <Input.Password autoComplete="new-password" maxLength={MAX_TOKEN_LEN} placeholder={isEdit ? KEEP_PLACEHOLDER : ''} />
        </Form.Item>
        <Form.Item name="remark" label="备注" rules={[{ max: MAX_REMARK_LEN, message: `最长 ${MAX_REMARK_LEN} 个字符` }]}>
          <Input autoComplete="off" />
        </Form.Item>
        <Button type="primary" htmlType="submit" loading={submit.isBusy}>
          {isEdit ? '保存' : '新建'}
        </Button>
      </Form>
    </>
  )
}
