/** Why trading is stopped, from the halt marker in the data directory. */
export interface Halt {
  reason: string
  /** What wrote it: "risk gate", "app" or "cli". */
  by: string
  at: string
}

export type Mode = 'dry-run' | 'live'

/** What Settings shows about the machine it is talking to. */
export interface SelfInfo {
  version: string
  /** The git ref a self-update would build by default. */
  ref: string
  mode: Mode
  halted: boolean
  halt: Halt | null
  now: string
}

export interface HaltStatus {
  halted: boolean
  halt: Halt | null
}

/** The paper book at a glance. Null on the overview until there is one. */
export interface Book {
  equity: number
  dayChange: number
  sinceStartPct: number
  drawdownPct: number
  dailyLossPct: number
  ordersToday: number
}

export type StrategyStatus = 'armed' | 'refused'

export interface StrategySummary {
  name: string
  status: StrategyStatus
  /** Why the runtime will not run it, when refused. */
  reason?: string
}

export interface RunSummary {
  runId: string
  status: string
  events: number
}

/** The dashboard in one round trip. */
export interface Overview {
  mode: Mode
  halted: boolean
  halt: Halt | null
  book: Book | null
  strategies: StrategySummary[]
  lastRun: RunSummary | null
}
