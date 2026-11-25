import { Button, Descriptions, Form, Radio, Select } from 'antd'
import { useCallback, useState } from 'react'
import { api, csrfHeader, type Agent, type EndUser, type PageData } from '../../api/client'
import { ErrorAlert } from '../../components/ErrorAlert'
import { FormModal } from '../../components/FormModal'
import { useRemote } from '../../hooks/use-remote'
import { useSubmit } from '../../hooks/use-submit'
import { MAX_KEYWORD_LEN } from '../form-limits'

// 用户的归属（docs/03 §5.23 名下 ④，Ruling-102）：按代理筛选与划转 / 收回；列代理要 admin:read，划转要 admin:write

/** 代理下拉一次取的条数；更多的靠输入用户名搜索。 */
const AGENT_OPTION_LIMIT = 20

interface AgentSelectProps {
  value?: number
  onChange?: (value: number | undefined) => void
  placeholder: string
  className?: string
}

/** 代理下拉：按用户名远程搜索（/api/admin/agents 的 keyword），停用的代理也列出并标明。 */
export function AgentSelect({ value, onChange, placeholder, className }: AgentSelectProps) {
  const [keyword, setKeyword] = useState('')
  const load = useCallback(
    (signal: AbortSignal) => api.GET('/api/admin/agents', { params: { query: { page: 1, pageSize: AGENT_OPTION_LIMIT, keyword: keyword === '' ? undefined : keyword } }, signal }),
    [keyword],
  )
  const agents = useRemote<PageData<Agent>>(load)
  const options = (agents.data?.items ?? []).map((a) => ({ value: a.id, label: a.isDisabled ? `${a.username}（已停用）` : a.username }))
  return (
    <Select
      allowClear
      showSearch={{ filterOption: false, onSearch: (v) => setKeyword(v.trim().slice(0, MAX_KEYWORD_LEN)) }}
      placeholder={placeholder}
      aria-label={placeholder}
      className={className}
      loading={agents.isLoading}
      value={value}
      options={options}
      onChange={(v: number | undefined) => onChange?.(v)}
    />
  )
}

type Target = 'platform' | 'agent'

interface TransferForm {
  target: Target
  agentId?: number
}

interface TransferModalProps {
  user: EndUser | null
  onClose: () => void
  onSaved: () => void
}

/** 划转给某个代理或收回为平台用户：只改归属，不动额度、邀请关系与流量池，不受代理的用户数上限限制。 */
export function TransferAgentModal({ user, onClose, onSaved }: TransferModalProps) {
  return (
    <FormModal title={`划转归属：${user?.username ?? ''}`} isOpen={user !== null} onClose={onClose}>
      {(generation) => user !== null && <TransferBody key={generation} user={user} onSaved={onSaved} />}
    </FormModal>
  )
}

function TransferBody({ user, onSaved }: { user: EndUser; onSaved: () => void }) {
  const [form] = Form.useForm<TransferForm>()
  const target = Form.useWatch('target', form)
  const { error, isBusy, run } = useSubmit('已更新归属', onSaved)
  const submit = (v: TransferForm) => {
    const agentId = v.target === 'agent' ? (v.agentId ?? null) : null
    return run(() => api.PUT('/api/admin/users/{id}/agent', { params: { ...csrfHeader(), path: { id: user.id } }, body: { agentId } }))
  }
  return (
    <>
      <ErrorAlert error={error} />
      <Descriptions size="small" column={1} className="page-alert">
        <Descriptions.Item label="当前归属">{user.agent ? `代理 ${user.agent.username}` : '平台'}</Descriptions.Item>
      </Descriptions>
      <Form<TransferForm> form={form} layout="vertical" initialValues={{ target: user.agent ? 'agent' : 'platform', agentId: user.agent?.id }} onFinish={(v) => void submit(v)}>
        <Form.Item name="target" label="划给">
          <Radio.Group
            optionType="button"
            options={[
              { value: 'platform', label: '平台（收回）' },
              { value: 'agent', label: '代理' },
            ]}
          />
        </Form.Item>
        {target === 'agent' && (
          <Form.Item name="agentId" label="代理" extra="可以划给已停用的代理；不受其用户数上限限制" rules={[{ required: true, message: '请选择代理' }]}>
            <AgentSelect placeholder="按用户名搜索代理" />
          </Form.Item>
        )}
        <Button type="primary" htmlType="submit" block loading={isBusy}>
          保存
        </Button>
      </Form>
    </>
  )
}
