import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { Form } from 'antd'
import { useEffect, useState } from 'react'
import { afterEach, describe, expect, it } from 'vitest'
import type { Binder } from './fields'
import { getAt, newDraft, setAt, toConfig, type InboundDraft } from './model'
import { TransportTab } from './transport-tab'

let latest: InboundDraft | null = null

/** 持有草稿的最小外壳：每次变化记到 latest 供断言。 */
function Harness() {
  const [draft, setDraft] = useState<InboundDraft>(() => newDraft('hysteria'))
  useEffect(() => {
    latest = draft
  }, [draft])
  const b: Binder = {
    draft,
    set: (path, value) => setDraft((d) => setAt(d, path, value)),
    update: (change) => setDraft((d) => change(d)),
    hints: null,
  }
  return (
    <Form layout="vertical">
      <TransportTab b={b} />
    </Form>
  )
}

afterEach(() => {
  cleanup()
  latest = null
})

describe('Hysteria2 的 Salamander 混淆', () => {
  it('混淆写在 finalmask.udp 里而不是 hysteriaSettings.auth；关掉后去掉', () => {
    render(<Harness />)
    expect(getAt(latest, ['streamSettings', 'hysteriaSettings', 'auth'])).toBeUndefined()
    fireEvent.click(screen.getByRole('switch', { name: 'Salamander 混淆' }))
    const mask = getAt(toConfig(latest as InboundDraft), ['streamSettings', 'finalmask', 'udp', 0]) as { type: string; settings: { password: string } }
    expect(mask.type).toBe('salamander')
    expect(mask.settings.password.length).toBeGreaterThanOrEqual(4)
    expect(screen.getByLabelText('混淆密码')).toBeDefined()

    fireEvent.click(screen.getByRole('switch', { name: 'Salamander 混淆' }))
    expect(getAt(toConfig(latest as InboundDraft), ['streamSettings', 'finalmask'])).toBeUndefined()
  })
})
