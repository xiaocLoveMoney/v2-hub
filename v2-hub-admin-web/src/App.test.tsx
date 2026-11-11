import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from './App'
import { APP_TITLE } from './app-config'

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('App', () => {
  it('后端在线时显示标题与在线状态', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => Response.json({ status: 'ok' })))
    render(<App />)
    expect(screen.getByRole('heading', { name: APP_TITLE })).toBeDefined()
    expect(await screen.findByText('后端在线')).toBeDefined()
  })

  it('后端返回非 2xx 时显示不可用', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 502 })))
    render(<App />)
    expect(await screen.findByText('后端不可用')).toBeDefined()
  })
})
