/** Markdown 正文的长度上限，与 hub-api.yaml 的 Markdown 一致。 */
export const MAX_MARKDOWN_LEN = 20000

/** 正文输入框下的说明：管理端只编辑源文本，渲染在用户端做（Ruling-25）。 */
export const MARKDOWN_HINT = 'Markdown 源文本；用户端渲染时不执行原始 HTML'

const MAX_ALT_LEN = 40
const DEFAULT_ALT = '图片'
// 图片说明里去掉会破坏 Markdown 图片语法的字符
const ALT_UNSAFE_CHARS = /[[\]()\\!\n\r]/g
const FILE_EXTENSION = /\.[^.]+$/

/** 由文件名得到图片说明：去扩展名、去掉 Markdown 特殊字符，空了就用“图片”。 */
export function altFromFileName(name: string): string {
  const alt = name.replace(FILE_EXTENSION, '').replace(ALT_UNSAFE_CHARS, ' ').trim().slice(0, MAX_ALT_LEN)
  return alt === '' ? DEFAULT_ALT : alt
}

/** 在 position 处插入一段图片引用，前后各补一个换行让它独占一行。 */
export function insertImage(text: string, position: number, alt: string, url: string): string {
  const at = Math.max(0, Math.min(position, text.length))
  const before = text.slice(0, at)
  const after = text.slice(at)
  const lead = before === '' || before.endsWith('\n') ? '' : '\n'
  const tail = after.startsWith('\n') ? '' : '\n'
  return `${before}${lead}![${alt}](${url})${tail}${after}`
}
