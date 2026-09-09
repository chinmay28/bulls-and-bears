import { describe, expect, it } from 'vitest'
import { invocationCount, latestInvocation } from './journal'
import type { JournalEvent } from '../types'

/** ev builds the one shape these functions read: a kind and a status. */
function ev(kind: string, status?: string): JournalEvent {
  return { ts: '2026-09-07T10:42:25Z', run_id: '2026-09-08', mode: 'paper', kind, data: status ? { status } : {} }
}

const start = () => ev('run', 'started')

describe('latestInvocation', () => {
  const cases: { name: string; events: JournalEvent[]; want: JournalEvent[] }[] = (() => {
    const firstStart = start()
    const failed = ev('run', 'failed')
    const secondStart = start()
    const armed = ev('strategy')
    const completed = ev('run', 'completed')
    const stray = ev('fill')
    return [
      { name: 'nothing at all', events: [], want: [] },
      {
        name: 'one invocation, from its own bracket',
        events: [firstStart, armed, completed],
        want: [firstStart, armed, completed],
      },
      {
        // The bug: a failed run at 10:42 and the re-run that worked at 12:53
        // share one file, and only the second one is still true.
        name: 'a failed run followed by a completed one keeps only the second',
        events: [firstStart, ev('error'), failed, secondStart, armed, completed],
        want: [secondStart, armed, completed],
      },
      {
        name: 'the last invocation is still running',
        events: [firstStart, failed, secondStart],
        want: [secondStart],
      },
      {
        // A journal read from the middle still has to show what it has.
        name: 'no opening bracket comes back whole',
        events: [stray, ev('error')],
        want: [stray, ev('error')],
      },
    ]
  })()

  for (const c of cases) {
    it(c.name, () => {
      expect(latestInvocation(c.events)).toEqual(c.want)
    })
  }

  it('does not mutate what it was given', () => {
    const events = [start(), ev('error'), start()]
    latestInvocation(events)
    expect(events).toHaveLength(3)
  })
})

describe('invocationCount', () => {
  const cases: { name: string; events: JournalEvent[]; want: number }[] = [
    { name: 'an empty journal', events: [], want: 0 },
    { name: 'one run', events: [start(), ev('run', 'completed')], want: 1 },
    { name: 'a re-run of the same date', events: [start(), ev('run', 'failed'), start(), ev('run', 'completed')], want: 2 },
    // Only "started" opens an invocation; the terminal brackets do not.
    { name: 'terminal run events are not starts', events: [ev('run', 'failed'), ev('run', 'halted')], want: 0 },
  ]

  for (const c of cases) {
    it(c.name, () => {
      expect(invocationCount(c.events)).toBe(c.want)
    })
  }
})
