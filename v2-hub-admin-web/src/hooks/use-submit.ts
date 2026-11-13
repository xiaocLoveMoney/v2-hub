import { App } from 'antd'
import { useState } from 'react'
import type { ApiError } from '../api/client'
import { mutate } from './use-remote'

type Action = () => Promise<{ error?: unknown }>

interface SubmitState {
  /** 最近一次提交的错误，留在弹窗里显示。 */
  error: ApiError | null
  isBusy: boolean
  /** 执行一次写操作：成功提示并回调，失败把错误留在 error。 */
  run: (action: Action) => Promise<void>
}

/**
 * 弹窗表单的提交：成功提示 okText 并调用 onSaved，失败不关弹窗。
 * @param okText 成功提示文案
 * @param onSaved 成功后的回调（关闭弹窗、刷新列表）
 */
export function useSubmit(okText: string, onSaved: () => void): SubmitState {
  const { message } = App.useApp()
  const [error, setError] = useState<ApiError | null>(null)
  const [isBusy, setIsBusy] = useState(false)
  const run = async (action: Action) => {
    setIsBusy(true)
    setError(null)
    const err = await mutate(action)
    setIsBusy(false)
    if (err !== null) {
      setError(err)
      return
    }
    void message.success(okText)
    onSaved()
  }
  return { error, isBusy, run }
}

/**
 * 表格行内的写操作（删除、启停等）：失败用全局提示，成功提示后回调。
 * @param onDone 成功后的回调（一般是刷新列表）
 * @returns 执行函数，成功返回 true
 */
export function useRowAction(onDone: () => void): (action: Action, okText: string) => Promise<boolean> {
  const { message } = App.useApp()
  return async (action, okText) => {
    const err = await mutate(action)
    if (err !== null) {
      void message.error(err.message)
      return false
    }
    void message.success(okText)
    onDone()
    return true
  }
}
