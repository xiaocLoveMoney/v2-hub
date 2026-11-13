import { useCallback, useEffect, useState } from 'react'

const TICK_MS = 1000

/**
 * 倒计时：start(秒) 后每秒减一到 0，卸载时清掉定时器。
 * @returns [剩余秒数, 开始倒计时的函数]
 */
export function useCountdown(): [number, (seconds: number) => void] {
  const [left, setLeft] = useState(0)
  useEffect(() => {
    if (left <= 0) {
      return undefined
    }
    const timer = setTimeout(() => setLeft((v) => v - 1), TICK_MS)
    return () => clearTimeout(timer)
  }, [left])
  const start = useCallback((seconds: number) => setLeft(seconds), [])
  return [left, start]
}
