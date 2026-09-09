import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import { RotationView } from './Rotation'
import type { Rotation } from '../types'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let mounted: { host: HTMLDivElement; root: Root } | null = null

function render(r: Rotation): string {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  act(() => {
    root.render(
      <MemoryRouter>
        <RotationView r={r} />
      </MemoryRouter>,
    )
  })
  mounted = { host, root }
  return host.textContent ?? ''
}

afterEach(() => {
  if (mounted) {
    act(() => mounted!.root.unmount())
    mounted.host.remove()
    mounted = null
  }
})

const base: Rotation = {
  configured: true,
  mode: 'flat',
  running: true,
  holderPid: 4242,
  risk: 'XLK',
  park: 'SATA',
  entryPrice: 0,
  entryDate: '',
  basis: 0,
  optionPnl: 0,
  dividends: 0,
  costs: 0,
  banked: 0,
  quickTargetPct: 0.0015,
  recoveryTargetPct: 0.01,
  recoveryTargetUsd: 0,
  breakEvenPrice: 0,
  shortCall: null,
  pending: null,
  ordersToday: 0,
  startOfDayEquity: 0,
  highWaterEquity: 0,
  dailyLimitHits: 0,
  marksDay: '',
  updatedAt: '2026-09-10T15:30:00Z',
  history: [],
  schedule: [{ name: 'Entry', pt: '07:12', utc: '14:45', cron: '45 14 * * 1-5', what: 'Open the lot.' }],
  rules: [{ label: 'Lot', value: 'exactly 100 shares' }],
}

const recovering: Rotation = {
  ...base,
  mode: 'recovery',
  entryPrice: 187,
  entryDate: '2026-09-09T14:45:00Z',
  basis: 18700,
  optionPnl: 85,
  dividends: 25,
  costs: 2,
  banked: 108,
  recoveryTargetUsd: 187,
  breakEvenPrice: 187.79,
  history: [{ at: '2026-09-10T15:30:00Z', mode: 'recovery', entry: 187, optionPnl: 85, strike: 191, pending: '' }],
}

describe('Rotation', () => {
  it('says a directory it has never run in is not broken', () => {
    const text = render({ ...base, configured: false, running: false })
    expect(text).toContain('never run here')
    expect(text).toContain('No lot has ever been opened')
    // The schedule and the rules show either way: they are what it would do.
    expect(text).toContain('Entry')
    expect(text).toContain('45 14 * * 1-5')
  })

  it('names the process holding the lock', () => {
    expect(render(base)).toContain('pid 4242')
  })

  it('shows what a recovering lot has banked against its target', () => {
    const text = render(recovering)
    expect(text).toContain('recovery')
    expect(text).toContain('$108')
    expect(text).toContain('$187')
    // The share price that closes the rest is the number to act on.
    expect(text).toContain('187.79')
    // SATA income is excluded by rule, and the screen says so.
    expect(text).toContain('SATA income is deliberately not counted')
  })

  it('marks a call that would not reach the target if assigned', () => {
    const bad = render({
      ...recovering,
      shortCall: { optionId: 'c1', strike: 188, expiration: '2026-09-11', credit: 0.85, assignedPct: 0.004 },
    })
    expect(bad).toContain('+0.4% if assigned')

    const good = render({
      ...recovering,
      shortCall: { optionId: 'c1', strike: 191, expiration: '2026-09-11', credit: 0.85, assignedPct: 0.0272 },
    })
    expect(good).toContain('+2.7% if assigned')
  })

  it('says when an order is out, and why nothing else goes', () => {
    const text = render({
      ...recovering,
      pending: {
        orderId: 'opt-1',
        kind: 'sell_call_to_open',
        symbol: 'XLK',
        strike: 191,
        placedAt: '2026-09-10T15:31:00Z',
      },
    })
    expect(text).toContain('sell call to open')
    expect(text).toContain('doubling the position')
  })

  it('does not show lot figures when the book is flat', () => {
    const text = render(base)
    expect(text).toContain('every idle dollar belongs in SATA')
    expect(text).not.toContain('Banked')
  })
})
