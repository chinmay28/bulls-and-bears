import { useState } from 'react'
import { api } from '../api'
import { Badge, Banner, Field, Sheet } from './ui'
import { time } from '../lib/format'
import type { Halt, Mode, StrategyStatus } from '../types'

export function ModeBadge({ mode }: { mode: Mode }) {
  return mode === 'live' ? <Badge tone="warn">Live</Badge> : <Badge tone="accent">Paper</Badge>
}

export function StrategyBadge({ status }: { status: StrategyStatus }) {
  switch (status) {
    case 'armed':
      return (
        <Badge tone="good" dot>
          Armed
        </Badge>
      )
    default:
      return (
        <Badge tone="warn" dot>
          Refused
        </Badge>
      )
  }
}

/** Why trading is stopped, in the words of whatever stopped it. */
export function HaltBanner({ halt }: { halt: Halt | null }) {
  if (!halt) return <Banner tone="bad">Halted. Nothing is placed until you resume.</Banner>
  return (
    <Banner tone="bad">
      <b>Halted</b> by the {halt.by} {halt.at ? `on ${time(halt.at)}` : ''}: {halt.reason}. Nothing is placed
      until you resume.
    </Banner>
  )
}

/** The confirmation before the marker is written. A reason is asked for
 *  because the marker outlives the memory of why. */
export function HaltSheet({ onClose, onHalted }: { onClose: () => void; onHalted: () => void }) {
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const halt = async () => {
    setBusy(true)
    try {
      await api.halt(reason.trim())
      onHalted()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setBusy(false)
    }
  }

  return (
    <Sheet
      title="Halt trading?"
      subtitle="Writes the halt marker the scheduler honours. Open positions are left as they are; nothing new is placed until you resume from here."
      onClose={onClose}
    >
      {error && <Banner tone="bad">{error}</Banner>}
      <Field label="Why" help="Optional. It is kept with the marker.">
        <input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Going away for a week" />
      </Field>
      <div className="actions">
        <button className="secondary" onClick={onClose} disabled={busy}>
          Cancel
        </button>
        <button className="danger" onClick={halt} disabled={busy}>
          {busy ? 'Halting…' : 'Halt'}
        </button>
      </div>
    </Sheet>
  )
}
