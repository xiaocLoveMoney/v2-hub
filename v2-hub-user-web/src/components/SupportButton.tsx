import { MessageCircle } from 'lucide-react'
import { useI18n } from '../i18n/i18n-context'
import { useSite } from '../site/site-context'

const ICON_SIZE = 18
/** 只接受 https 外链（Ruling-44）；后端已校验，这里再挡一次，不是就不显示。 */
const HTTPS_PREFIX = 'https://'

/** 右下角的客服入口：新窗口打开站点设置里的外链，不嵌入任何第三方脚本或 iframe。 */
export function SupportButton() {
  const { support } = useSite()
  const { t } = useI18n()
  const url = support?.url ?? ''
  if (!url.startsWith(HTTPS_PREFIX)) {
    return null
  }
  const label = support?.label === undefined || support.label === '' ? t('support.defaultLabel') : support.label
  return (
    <a className="hub-support" href={url} target="_blank" rel="noopener noreferrer">
      <MessageCircle size={ICON_SIZE} aria-hidden="true" />
      <span>{label}</span>
    </a>
  )
}
