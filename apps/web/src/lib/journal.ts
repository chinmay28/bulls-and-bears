/** Reading a run's event list the way the file was written.
 *
 *  A journal is one JSONL file per trading date and the writer appends to it:
 *  reopening a run id never truncates (server/internal/journal, `Open`). So one
 *  run id can hold several invocations — a run that failed at 10:42 and the
 *  re-run that succeeded at 12:53 are the same file — each opened by a `run`
 *  event with status "started".
 *
 *  That splits what the page can say into two kinds. What *happened* under this
 *  date (how many events, orders, fills) reads every invocation. What the run
 *  *is* — what it refused, what it needs next — reads the last invocation
 *  alone, or it reports a failure the next run already put right.
 */
import type { JournalEvent } from '../types'

/** started marks a `run` event that opens an invocation. */
function started(e: JournalEvent): boolean {
  return e.kind === 'run' && (e.data as { status?: string } | undefined)?.status === 'started'
}

/** invocationCount is how many times this run id was run. More than one is why
 *  a journal's badge (the last invocation's status) and its counts (the totals)
 *  can disagree. */
export function invocationCount(events: JournalEvent[]): number {
  return events.filter(started).length
}

/** latestInvocation is the events of the last run under this id: the run as it
 *  stands now. A journal with no opening bracket at all — read from the middle,
 *  or written before the runner bracketed its runs — comes back whole rather
 *  than empty, so a caller never loses events it would otherwise have seen. */
export function latestInvocation(events: JournalEvent[]): JournalEvent[] {
  for (let i = events.length - 1; i >= 0; i--) {
    if (started(events[i])) return events.slice(i)
  }
  return events
}
