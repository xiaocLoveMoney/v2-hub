import { Modal } from 'antd'
import type { ReactNode } from 'react'
import { useOpenGeneration } from '../hooks/use-open-generation'

interface FormModalProps {
  title: ReactNode
  isOpen: boolean
  onClose: () => void
  /** 不传用 antd 默认宽度。 */
  width?: number
  /** 渲染弹窗内容；generation 每次打开加一，用作表单组件的 key，让每次打开都是全新的表单。 */
  children: (generation: number) => ReactNode
}

/**
 * 表单弹窗：无底栏、关闭即销毁，内容按打开次数重建（见 useOpenGeneration）。
 * @param title 标题
 * @param isOpen 是否打开
 * @param onClose 关闭（取消、点遮罩、按 Esc）
 * @param width 宽度
 * @param children 内容渲染函数，参数是本次打开的序号
 */
export function FormModal({ title, isOpen, onClose, width, children }: FormModalProps) {
  const generation = useOpenGeneration(isOpen)
  return (
    <Modal title={title} open={isOpen} onCancel={onClose} footer={null} destroyOnHidden width={width}>
      {children(generation)}
    </Modal>
  )
}
