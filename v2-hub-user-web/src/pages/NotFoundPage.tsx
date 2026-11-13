import { Card, Empty, PageHeader } from '../components/ui'

/** 404 页。 */
export function NotFoundPage() {
  return (
    <>
      <PageHeader title="页面不存在" />
      <Card>
        <Empty>你访问的页面不存在或已被移除。</Empty>
      </Card>
    </>
  )
}
