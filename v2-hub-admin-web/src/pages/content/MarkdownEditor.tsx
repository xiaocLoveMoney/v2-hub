import { PictureOutlined } from '@ant-design/icons'
import { App, Button, Input } from 'antd'
import type { TextAreaRef } from 'antd/es/input/TextArea'
import { useRef, useState, type ChangeEvent } from 'react'
import { api, csrfHeader } from '../../api/client'
import { mutate } from '../../hooks/use-remote'
import { altFromFileName, insertImage, MAX_MARKDOWN_LEN } from './markdown'

/** 与服务端一致的限制（docs/03 §5.9，Ruling-89）；这里只为提前提示，以服务端校验为准。 */
const ACCEPTED_IMAGE_TYPES = 'image/png,image/jpeg,image/gif,image/webp'
const MAX_IMAGE_BYTES = 5 * 1024 * 1024
const MAX_IMAGE_MIB = MAX_IMAGE_BYTES / 1024 / 1024

interface MarkdownEditorProps {
  /** Form.Item 注入的 id（让标签指向文本框）、值与回调。 */
  id?: string
  value?: string
  onChange?: (value: string) => void
  rows: number
}

/**
 * 公告 / 教程的 Markdown 正文框：多行文本 + “插入图片”。
 * 选图后经 POST /api/admin/uploads 上传，在光标处插入 `![说明](/uploads/…)`（Ruling-89）。
 */
export function MarkdownEditor({ id, value = '', onChange, rows }: MarkdownEditorProps) {
  const { message } = App.useApp()
  const textAreaRef = useRef<TextAreaRef>(null)
  const fileRef = useRef<HTMLInputElement>(null)
  const [isUploading, setIsUploading] = useState(false)

  const upload = async (file: File) => {
    if (file.size > MAX_IMAGE_BYTES) {
      void message.error(`图片不能超过 ${MAX_IMAGE_MIB} MiB`)
      return
    }
    // 上传期间文本可能变，先记下光标
    const cursor = textAreaRef.current?.resizableTextArea?.textArea.selectionStart ?? value.length
    setIsUploading(true)
    let url = ''
    const err = await mutate(async () => {
      const res = await api.POST('/api/admin/uploads', {
        params: csrfHeader(),
        body: { file: '' },
        bodySerializer: () => {
          const form = new FormData()
          form.append('file', file)
          return form
        },
      })
      url = res.data?.data?.url ?? ''
      return res
    })
    setIsUploading(false)
    if (err !== null) {
      void message.error(err.message)
      return
    }
    onChange?.(insertImage(value, cursor, altFromFileName(file.name), url))
    void message.success('已插入图片')
  }

  const onFile = (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    // 清空以便连续选同一个文件
    e.target.value = ''
    if (file !== undefined) {
      void upload(file)
    }
  }

  return (
    <div className="markdown-editor">
      <Input.TextArea
        id={id}
        ref={textAreaRef}
        rows={rows}
        className="mono"
        showCount
        maxLength={MAX_MARKDOWN_LEN}
        value={value}
        onChange={(e) => onChange?.(e.target.value)}
      />
      <div className="markdown-editor-toolbar">
        <Button size="small" icon={<PictureOutlined />} loading={isUploading} onClick={() => fileRef.current?.click()}>
          插入图片
        </Button>
        <input ref={fileRef} type="file" accept={ACCEPTED_IMAGE_TYPES} hidden aria-label="选择图片" onChange={onFile} />
      </div>
    </div>
  )
}
