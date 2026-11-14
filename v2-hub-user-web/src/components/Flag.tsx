import { Globe } from 'lucide-react'
import { useI18n } from '../i18n/i18n-context'
import { countryName } from '../format'
import { LOCALE_TAG } from '../i18n/locale'

/**
 * 国旗（Ruling-65）：SVG 取自 flag-icons（MIT），不依赖系统的 emoji 国旗字体（Windows 上没有）。
 * 每面旗是单独的静态文件、不内联进包，只在用到时才下载。
 */
const FLAG_URLS = import.meta.glob<string>('/node_modules/flag-icons/flags/4x3/*.svg', { query: '?no-inline', import: 'default', eager: true })
const FLAG_FILE = /\/([a-z]{2})\.svg$/

const flagByCode = new Map(
  Object.entries(FLAG_URLS).flatMap(([path, url]) => {
    const code = FLAG_FILE.exec(path)?.[1]
    return code === undefined ? [] : [[code.toUpperCase(), url] as const]
  }),
)

const FLAG_HEIGHT = 12
const FLAG_WIDTH = 16

/** 按两位国家码显示国旗；未知或没有对应旗帜时显示地球图标。 */
export function Flag({ code }: { code: string }) {
  const { locale } = useI18n()
  const url = flagByCode.get(code)
  if (url === undefined) {
    return <Globe size={14} className="hub-flag-unknown" aria-hidden="true" />
  }
  const name = countryName(code, LOCALE_TAG[locale])
  return <img className="hub-flag" src={url} width={FLAG_WIDTH} height={FLAG_HEIGHT} alt={name} title={name} />
}
