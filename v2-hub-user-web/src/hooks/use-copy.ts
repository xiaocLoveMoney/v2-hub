import { useCallback } from 'react'
import { useToast } from '../components/toast-context'
import { useI18n } from '../i18n/i18n-context'

/** 返回"复制到剪贴板并提示结果"的函数；okText 不传时提示“已复制”，浏览器不允许时提示手动复制。 */
export function useCopy(): (text: string, okText?: string) => Promise<void> {
  const toast = useToast()
  const { t } = useI18n()
  return useCallback(
    async (text: string, okText?: string) => {
      try {
        await navigator.clipboard.writeText(text)
        toast.show(okText ?? t('common.copied'), 'success')
      } catch (err) {
        reportError(err)
        toast.show(t('common.copyFailed'), 'error')
      }
    },
    [toast, t],
  )
}
