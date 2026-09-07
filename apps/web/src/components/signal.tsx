import type { Signal } from '../types'

/** ZGauge draws where the spread's z-score sits against the entry and exit
 *  bands: a track from −3σ to +3σ, the exit band shaded, ticks at the entry
 *  thresholds, and a dot for now. */
export function ZGauge({ signal }: { signal: Signal }) {
  const w = 330
  const x = (z: number) => 10 + ((Math.max(-3, Math.min(3, z)) + 3) / 6) * (w - 20)
  const { entryZ, exitZ } = signal
  return (
    <svg viewBox={`0 0 ${w} 40`} width="100%" height="40" aria-hidden="true" style={{ display: 'block', marginTop: 6 }}>
      <line x1={10} y1={20} x2={w - 10} y2={20} stroke="var(--border)" strokeWidth={6} strokeLinecap="round" />
      <line x1={x(-entryZ)} y1={20} x2={x(entryZ)} y2={20} stroke="var(--accent-soft)" strokeWidth={6} strokeLinecap="round" />
      <g stroke="var(--text-dim)" strokeWidth={1.5}>
        <line x1={x(-entryZ)} y1={10} x2={x(-entryZ)} y2={30} />
        <line x1={x(-exitZ)} y1={12} x2={x(-exitZ)} y2={28} />
        <line x1={x(exitZ)} y1={12} x2={x(exitZ)} y2={28} />
        <line x1={x(entryZ)} y1={10} x2={x(entryZ)} y2={30} />
      </g>
      {signal.zDefined && <circle cx={x(signal.z)} cy={20} r={7} fill={signal.state === 0 ? 'var(--accent)' : signal.state > 0 ? 'var(--good)' : 'var(--bad)'} />}
      <g fontFamily="ui-monospace, Menlo, monospace" fontSize={10} fill="var(--text-dim)" textAnchor="middle">
        <text x={x(-entryZ)} y={39}>
          −{entryZ}
        </text>
        <text x={x(-exitZ)} y={39}>
          −{exitZ}
        </text>
        <text x={x(exitZ)} y={39}>
          +{exitZ}
        </text>
        <text x={x(entryZ)} y={39}>
          +{entryZ}
        </text>
      </g>
    </svg>
  )
}

/** One sentence about what the signal is doing. */
export function describeSignal(s: Signal, universe: string[] = []): string {
  if (!s.zDefined) return 'z-score undefined: not enough bars, or a flat spread.'
  const [a, b] = universe
  switch (s.state) {
    case 1:
      return `Long the spread${a && b ? `: long ${a}, short ${b}` : ''}. Exits above −${s.exitZ}.`
    case -1:
      return `Short the spread${a && b ? `: short ${a}, long ${b}` : ''}. Exits below +${s.exitZ}.`
    default:
      return `Flat, inside the band. Enters below −${s.entryZ} or above +${s.entryZ}.`
  }
}

/** Sparkline draws an equity curve; flat when there is nothing to show. */
export function EquityLine({ values, height = 44 }: { values: number[]; height?: number }) {
  if (values.length < 2) {
    return <div className="sub" style={{ height, display: 'grid', placeItems: 'center' }}>Not enough history yet</div>
  }
  const width = 300
  const lo = Math.min(...values)
  const hi = Math.max(...values)
  const span = hi - lo || 1
  const step = width / (values.length - 1)
  const pts = values.map((v, i) => `${(i * step).toFixed(1)},${(height - ((v - lo) / span) * (height - 4) - 2).toFixed(1)}`)
  return (
    <svg className="sparkline" viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" aria-hidden="true" style={{ height }}>
      <path d={`M0,${height} L${pts.join(' L')} L${width},${height} Z`} fill="var(--accent-soft)" />
      <polyline points={pts.join(' ')} fill="none" stroke="var(--accent)" strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" />
    </svg>
  )
}
