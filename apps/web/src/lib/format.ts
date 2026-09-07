/** Formatting helpers, tuned for a small screen: short and unambiguous. */

export function ago(iso: string | null | undefined): string {
  if (!iso) return 'never'
  const seconds = Math.round((Date.now() - new Date(iso).getTime()) / 1000)
  if (seconds < 45) return 'just now'
  if (seconds < 90) return 'a minute ago'
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.round(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.round(hours / 24)
  if (days < 30) return `${days}d ago`
  return new Date(iso).toLocaleDateString()
}

export function time(iso: string): string {
  return new Date(iso).toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
  })
}

/** Dollars the way a statement prints them: two places, thousands separated,
 *  a sign only when asked for. */
export function usd(n: number, signed = false): string {
  const abs = Math.abs(n).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })
  if (n < 0) return `−$${abs}`
  return signed && n > 0 ? `+$${abs}` : `$${abs}`
}

/** A fraction as a percentage with one place, "−" for negatives so it reads
 *  as a number rather than a hyphen. */
export function pct(fraction: number, signed = false): string {
  const v = fraction * 100
  const s = `${Math.abs(v).toFixed(1)}%`
  if (v < 0) return `−${s}`
  return signed && v > 0 ? `+${s}` : s
}

export function percent(used: number, total: number): number {
  if (!total) return 0
  return Math.min(100, Math.max(0, (used / total) * 100))
}

/** severity turns a usage percentage into a bar colour class. */
export function severity(pct: number): '' | 'warn' | 'bad' {
  if (pct >= 90) return 'bad'
  if (pct >= 75) return 'warn'
  return ''
}

/** parts joins the pieces of a card's subtitle, dropping the ones that had
 *  nothing to say. */
export function parts(...pieces: (string | false | null | undefined)[]): string {
  return pieces.filter(Boolean).join(' · ')
}
