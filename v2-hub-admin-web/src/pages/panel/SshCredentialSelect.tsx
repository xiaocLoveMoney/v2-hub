import { Alert, Select } from 'antd'
import { api, type SshCredential } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { useRemote } from '../../hooks/use-remote'
import { SSH_AUTH_TYPE_LABELS } from './host-labels'

const loadCredentials = (signal: AbortSignal) => api.GET('/api/admin/ssh-credentials', { signal })

interface SshCredentialSelectProps {
  /** 由 Form.Item 注入，用来把标签关联到下拉框。 */
  id?: string
  value?: number
  onChange?: (value: number) => void
}

/**
 * 凭据下拉框（表单控件，放在 Form.Item 里用）：每次挂载取一次凭据列表，只显示名称、用户名与认证方式；
 * 一份凭据都没有时提示先去新建（docs/03 §5.22 SSH 凭据）。
 */
export function SshCredentialSelect({ id, value, onChange }: SshCredentialSelectProps) {
  const credentials = useRemote<SshCredential[]>(loadCredentials)
  const options = (credentials.data ?? []).map((c) => ({ value: c.id, label: `${c.name}（${c.username}，${SSH_AUTH_TYPE_LABELS[c.authType]}）` }))
  const isEmpty = credentials.data !== null && credentials.data.length === 0
  return (
    <>
      <ErrorAlert error={credentials.error} />
      <Select id={id} options={options} value={value} onChange={onChange} loading={credentials.isLoading} placeholder="选择 SSH 凭据" />
      {isEmpty && <Alert type="info" showIcon className="form-hint" title="还没有 SSH 凭据，请先在“SSH 凭据”里新建一份" />}
    </>
  )
}
