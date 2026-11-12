import { useState } from 'react'

/**
 * 弹窗每打开一次返回一个新的序号，用作弹窗内表单组件的 key：每次打开都是全新的表单实例与状态。
 * 不依赖 destroyOnHidden 的销毁时机——关闭动画还没结束就再次打开时，弹窗内容不会被重建。
 * @param isOpen 弹窗当前是否打开
 * @returns 从 0 开始、每次由关到开加一的序号
 */
export function useOpenGeneration(isOpen: boolean): number {
  const [wasOpen, setWasOpen] = useState(isOpen)
  const [generation, setGeneration] = useState(0)
  // 渲染期间按上一次的 isOpen 调整状态（React 文档推荐的写法），避免先用旧表单渲染一帧再重建
  if (isOpen !== wasOpen) {
    setWasOpen(isOpen)
    if (isOpen) {
      setGeneration(generation + 1)
    }
  }
  return generation
}
