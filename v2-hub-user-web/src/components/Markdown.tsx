import ReactMarkdown, { type Components } from 'react-markdown'

// 链接一律新窗口打开，且不把本站地址与窗口句柄交给对方
const COMPONENTS: Components = {
  a: ({ href, children }) => (
    <a href={href} target="_blank" rel="noopener noreferrer">
      {children}
    </a>
  ),
}

interface MarkdownProps {
  /** Markdown 源文本（公告、教程正文）。 */
  source: string
}

/**
 * 把 Markdown 渲染成 React 元素（Ruling-25）：不解析原始 HTML（skipHtml 直接丢弃），不经 innerHTML；
 * 链接与图片地址用 react-markdown 默认的地址过滤，去掉 javascript: 等危险协议。
 */
export function Markdown({ source }: MarkdownProps) {
  return (
    <div className="hub-markdown">
      <ReactMarkdown skipHtml components={COMPONENTS}>
        {source}
      </ReactMarkdown>
    </div>
  )
}
