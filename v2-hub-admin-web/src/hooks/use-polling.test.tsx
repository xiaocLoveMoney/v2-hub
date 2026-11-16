import { cleanup, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { HOST_TASK_POLL_INTERVAL_MS, usePollRemote, usePolling } from './use-polling'

const TICKS = 3

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe('usePolling', () => {
  it('条件为真时按间隔调用回调，默认间隔 2 秒', () => {
    const callback = vi.fn()
    renderHook(() => usePolling(callback, true))
    expect(callback).not.toHaveBeenCalled()
    vi.advanceTimersByTime(HOST_TASK_POLL_INTERVAL_MS - 1)
    expect(callback).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    expect(callback).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(HOST_TASK_POLL_INTERVAL_MS * (TICKS - 1))
    expect(callback).toHaveBeenCalledTimes(TICKS)
    expect(HOST_TASK_POLL_INTERVAL_MS).toBe(2000)
  })

  it('条件为假时不调用；由真变假即停，再变真重新开始', () => {
    const callback = vi.fn()
    const { rerender } = renderHook(({ isActive }) => usePolling(callback, isActive), { initialProps: { isActive: false } })
    vi.advanceTimersByTime(HOST_TASK_POLL_INTERVAL_MS * TICKS)
    expect(callback).not.toHaveBeenCalled()

    rerender({ isActive: true })
    vi.advanceTimersByTime(HOST_TASK_POLL_INTERVAL_MS)
    expect(callback).toHaveBeenCalledTimes(1)

    rerender({ isActive: false })
    vi.advanceTimersByTime(HOST_TASK_POLL_INTERVAL_MS * TICKS)
    expect(callback).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(0)

    rerender({ isActive: true })
    vi.advanceTimersByTime(HOST_TASK_POLL_INTERVAL_MS)
    expect(callback).toHaveBeenCalledTimes(2)
  })

  it('组件卸载即清掉定时器', () => {
    const callback = vi.fn()
    const { unmount } = renderHook(() => usePolling(callback, true))
    vi.advanceTimersByTime(HOST_TASK_POLL_INTERVAL_MS)
    expect(callback).toHaveBeenCalledTimes(1)
    unmount()
    expect(vi.getTimerCount()).toBe(0)
    vi.advanceTimersByTime(HOST_TASK_POLL_INTERVAL_MS * TICKS)
    expect(callback).toHaveBeenCalledTimes(1)
  })

  it('总是调用最近一次渲染传入的回调，换回调不重置计时', () => {
    const first = vi.fn()
    const second = vi.fn()
    const { rerender } = renderHook(({ callback }) => usePolling(callback, true), { initialProps: { callback: first } })
    vi.advanceTimersByTime(HOST_TASK_POLL_INTERVAL_MS / 2)
    rerender({ callback: second })
    vi.advanceTimersByTime(HOST_TASK_POLL_INTERVAL_MS / 2)
    expect(first).not.toHaveBeenCalled()
    expect(second).toHaveBeenCalledTimes(1)
  })
})

describe('usePollRemote', () => {
  it('每轮刷新各份数据，上一次还没回来的那份跳过', () => {
    const idle = { isLoading: false, reload: vi.fn() }
    const busy = { isLoading: true, reload: vi.fn() }
    renderHook(() => usePollRemote([idle, busy], true))
    vi.advanceTimersByTime(HOST_TASK_POLL_INTERVAL_MS * TICKS)
    expect(idle.reload).toHaveBeenCalledTimes(TICKS)
    expect(busy.reload).not.toHaveBeenCalled()
  })

  it('条件为假时不刷新', () => {
    const remote = { isLoading: false, reload: vi.fn() }
    renderHook(() => usePollRemote([remote], false))
    vi.advanceTimersByTime(HOST_TASK_POLL_INTERVAL_MS * TICKS)
    expect(remote.reload).not.toHaveBeenCalled()
  })
})
