import { Select } from 'antd'
import type { DnsAccount } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useRemote } from '../../hooks/use-remote'
import { loadDnsAccounts } from './host-actions'
import { DNS_PROVIDER_LABELS } from './host-labels'

interface DnsAccountSelectProps {
  /** 由 Form.Item 注入，用来把标签关联到下拉框。 */
  id?: string
  /** 选中的 DNS 账号；null 或不带表示不用。 */
  value?: number | null
  onChange?: (value: number | null) => void
}

/**
 * DNS 账号下拉框（表单控件，放在 Form.Item 里用；可清空 = 不申请公开证书）：
 * 每次挂载取一次账号列表，只显示名称、服务商与根域名（docs/03 §5.22 DNS 账号）。
 */
export function DnsAccountSelect({ id, value, onChange }: DnsAccountSelectProps) {
  const accounts = useRemote<DnsAccount[]>(loadDnsAccounts)
  const options = (accounts.data ?? []).map((a) => ({ value: a.id, label: `${a.name}（${DNS_PROVIDER_LABELS[a.providerType] ?? a.providerType}，${a.zone}）` }))
  return (
    <>
      <ErrorAlert error={accounts.error} />
      <Select
        id={id}
        allowClear
        options={options}
        value={value ?? undefined}
        onChange={(next: number | undefined) => onChange?.(next ?? null)}
        loading={accounts.isLoading}
        placeholder="不选 = 面板用自签证书"
      />
    </>
  )
}
