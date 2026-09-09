import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import { NextStep } from './RunDetail'
import { latestInvocation } from '../lib/journal'
import type { JournalEvent } from '../types'

// React's act() wants to be told it is running under a test harness.
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let mounted: { host: HTMLDivElement; root: Root } | null = null

/** render puts NextStep on a page the way RunDetail does — over the last
 *  invocation, not the whole file — and hands back its text. */
function render(events: JournalEvent[]): string {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  act(() => {
    root.render(
      <MemoryRouter>
        <NextStep events={latestInvocation(events)} />
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

function ev(kind: string, data: Record<string, unknown>): JournalEvent {
  return { ts: '2026-09-07T10:42:25Z', run_id: '2026-09-08', mode: 'paper', kind, data }
}

const startEv = () => ev('run', { status: 'started' })
const noArmed = 'no armed strategy: nothing to trade'

describe('the next step on a run', () => {
  it('offers Strategies when the run really did arm nothing', () => {
    const text = render([startEv(), ev('error', { error: noArmed }), ev('run', { status: 'failed', reason: noArmed })])
    expect(text).toContain('Nothing was armed')
    expect(text).toContain('Go to Strategies')
  })

  it('sends the operator to Settings when the bars were the problem', () => {
    const bars = 'bars: SPY is stale: last bar 2026-09-01, limit 3 trading days'
    const text = render([startEv(), ev('error', { error: bars }), ev('run', { status: 'failed', reason: bars })])
    expect(text).toContain('The bars were not good enough to trade on')
    expect(text).toContain('Refresh bars in Settings')
  })

  it('says nothing about a failure the re-run already put right', () => {
    // The reported bug: 10:42 armed nothing, 12:53 armed and completed, and
    // both are in 2026-09-08.jsonl. The banner belongs to neither the badge
    // ("Completed") nor the journal's last word, so it must not appear.
    const text = render([
      startEv(),
      ev('error', { error: noArmed }),
      ev('run', { status: 'failed', reason: noArmed }),
      startEv(),
      ev('strategy', { name: 'dual_momentum', armed: true }),
      ev('run', { status: 'completed' }),
    ])
    expect(text).toBe('')
  })

  it('still speaks when the re-run failed the same way', () => {
    const text = render([
      startEv(),
      ev('error', { error: noArmed }),
      ev('run', { status: 'failed', reason: noArmed }),
      startEv(),
      ev('error', { error: noArmed }),
      ev('run', { status: 'failed', reason: noArmed }),
    ])
    expect(text).toContain('Nothing was armed')
  })

  it('has nothing to add to a clean run', () => {
    expect(render([startEv(), ev('run', { status: 'completed' })])).toBe('')
  })
})
