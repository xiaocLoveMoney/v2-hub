import { useState } from 'react'
import { api, type SharedAccount } from '../api/client'
import { useToast } from '../components/toast-context'
import { Alert, Button, Card, Empty, PageHeader } from '../components/ui'
import { useCopy } from '../hooks/use-copy'
import { request, useRemote } from '../hooks/use-remote'
import { useI18n } from '../i18n/i18n-context'

const loadAccounts = (signal: AbortSignal) => api.GET('/api/user/shared-accounts', { signal })

/** 按分类归组，保持后端给的顺序。 */
function groupByCategory(accounts: SharedAccount[]): { category: string; items: SharedAccount[] }[] {
  const groups: { category: string; items: SharedAccount[] }[] = []
  for (const account of accounts) {
    const group = groups.find((g) => g.category === account.category)
    if (group === undefined) {
      groups.push({ category: account.category, items: [account] })
      continue
    }
    group.items.push(account)
  }
  return groups
}

/** 一条共享账号：密码属于凭据，默认不取，点“显示”才单独请求一次（后端记审计），只留在本组件内存里。 */
function AccountRow({ account }: { account: SharedAccount }) {
  const { t } = useI18n()
  const toast = useToast()
  const copy = useCopy()
  const [credential, setCredential] = useState<string | null>(null)
  const [isBusy, setIsBusy] = useState(false)

  const reveal = async () => {
    setIsBusy(true)
    try {
      const result = await request(() => api.GET('/api/user/shared-accounts/{id}/credential', { params: { path: { id: account.id } } }))
      if (result.error !== null) {
        toast.show(result.error.message, 'error')
        return
      }
      setCredential(result.data.credential)
    } finally {
      setIsBusy(false)
    }
  }

  return (
    <div className="hub-shared-item">
      <div className="hub-shared-name">{account.name}</div>
      <dl className="hub-shared-fields">
        <dt>{t('shared.account')}</dt>
        <dd>
          <span className="hub-mono">{account.account}</span>
          <Button variant="outline" onClick={() => void copy(account.account, t('shared.accountCopied'))}>
            {t('common.copy')}
          </Button>
        </dd>
        <dt>{t('shared.credential')}</dt>
        <dd>
          {credential === null ? (
            <Button variant="outline" isBusy={isBusy} onClick={() => void reveal()}>
              {t('shared.show')}
            </Button>
          ) : (
            <>
              <span className="hub-mono">{credential}</span>
              <Button variant="outline" onClick={() => void copy(credential, t('shared.credentialCopied'))}>
                {t('common.copy')}
              </Button>
              <Button variant="outline" onClick={() => setCredential(null)}>
                {t('shared.hide')}
              </Button>
            </>
          )}
        </dd>
      </dl>
      {account.remark !== '' && <p className="hub-field-hint">{account.remark}</p>}
    </div>
  )
}

/** 共享账号（docs/03 §5.9）：按分类分卡；只有状态正常且套餐符合的用户能看到条目。 */
export function SharedAccountsPage() {
  const { t } = useI18n()
  const accounts = useRemote<SharedAccount[]>(loadAccounts)
  const groups = groupByCategory(accounts.data ?? [])

  return (
    <>
      <PageHeader title={t('menu.sharedAccounts')} />
      {accounts.error !== null && <Alert tone="plain">{accounts.error.message}</Alert>}
      {accounts.data !== null && groups.length === 0 && (
        <Card>
          <Empty>{t('shared.empty')}</Empty>
        </Card>
      )}
      <div className="hub-grid-2">
        {groups.map((g) => (
          <Card key={g.category} title={g.category}>
            {g.items.map((a) => (
              <AccountRow key={a.id} account={a} />
            ))}
          </Card>
        ))}
      </div>
    </>
  )
}
