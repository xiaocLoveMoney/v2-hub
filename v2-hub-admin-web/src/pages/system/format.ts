const EMPTY = '—'

/** 表格按内容宽度排版、窄屏横向滚动，避免短列被挤成竖排。 */
export const TABLE_SCROLL = { x: 'max-content' } as const

const timeFormatter = new Intl.DateTimeFormat('zh-CN', {
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hour12: false,
})

/** 把 ISO 时间格式化为本地时间；空值显示占位符。 */
export function formatTime(value: string | null | undefined): string {
  if (value === null || value === undefined || value === '') {
    return EMPTY
  }
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? EMPTY : timeFormatter.format(date)
}

/** 审计详情以纯文本 JSON 展示（不经 innerHTML，防 XSS）。 */
export function formatDetail(detail: unknown): string {
  if (detail === null || detail === undefined) {
    return EMPTY
  }
  return JSON.stringify(detail)
}
