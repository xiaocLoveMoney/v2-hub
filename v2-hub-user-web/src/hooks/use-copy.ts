import { useCallback } from 'react'
import { useToast } from '../components/toast-context'

/** 返回"复制到剪贴板并提示结果"的函数；浏览器不允许时提示手动复制。 */
export function useCopy(): (text: string, okText?: string) => Promise<void> {
  const toast = useToast()
  return useCallback(
    async (text: string, okText = '已复制') => {
      try {
        await navigator.clipboard.writeText(text)
        toast.show(okText, 'success')
      } catch (err) {
        reportError(err)
        toast.show('复制失败，请手动选中复制', 'error')
      }
    },
    [toast],
  )
}
