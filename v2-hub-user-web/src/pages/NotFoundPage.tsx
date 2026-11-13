import { Card, Empty, PageHeader } from '../components/ui'
import { useI18n } from '../i18n/i18n-context'

/** 404 页。 */
export function NotFoundPage() {
  const { t } = useI18n()
  return (
    <>
      <PageHeader title={t('shell.notFoundTitle')} />
      <Card>
        <Empty>{t('shell.notFoundBody')}</Empty>
      </Card>
    </>
  )
}
