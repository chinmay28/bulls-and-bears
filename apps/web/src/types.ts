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

/** The paper book at a glance: the money, and how close the guardrails are. */
export interface BookSummary {
  equity: number
  startingCash: number
  startOfDay: number
  highWater: number
  dayChange: number
  sinceStartPct: number
  drawdownPct: number
  dailyLossPct: number
  ordersToday: number
  positions: number
  gross: number
  openedAt: string
}

export type StrategyStatus = 'armed' | 'refused' | 'invalid'

export interface Signal {
  date: string
  z: number
  zDefined: boolean
  /** +1 long the spread, −1 short, 0 flat. */
  state: number
  entryZ: number
  exitZ: number
  weights: Record<string, number>
  lastBar: string
  stale: boolean
}

export interface Provenance {
  trainFrom: string
  trainTo: string
  testFrom: string
  testTo: string
  oosSharpe: number
  oosMaxDrawdown: number
  commissionUsd: number
  slippageBps: number
  researchGitSha: string
  generatedAt: string
  ttlDays: number
}

export interface Strategy {
  name: string
  path: string
  status: StrategyStatus
  /** Why the runtime will not run it, when refused or invalid. */
  reason?: string
  strategy?: string
  universe?: string[]
  params?: Record<string, number>
  sizing?: { grossLeverage: number; maxNotionalPerLegUsd: number }
  provenance?: Provenance
  freshDays: number
  expires?: string
  signal: Signal | null
  signalError?: string
}

export interface Backtest {
  name: string
  from: string
  to: string
  bars: number
  trades: number
  sharpe: number | null
  maxDrawdown: number
  maxDrawdownDuration: number
  totalReturn: number
  equity: { date: string; equity: number }[]
  specSharpe: number
}

export interface Run {
  runId: string
  mode: string
  status: string
  events: number
  orders: number
  fills: number
  rejected: number
  errors: number
  startedAt: string
  endedAt: string
  /** The journal checker's complaint, when an order lacked an allowed decision. */
  invariant?: string
}

export interface JournalEvent {
  ts: string
  run_id: string
  mode: string
  kind: string
  intent_id?: string
  allowed?: boolean
  data?: unknown
}

export interface RunDetail {
  run: Run
  events: JournalEvent[]
  truncated: boolean
}

export interface Position {
  symbol: string
  qty: number
  avgCost: number
  mark: number
  marketValue: number
  unrealizedPnl: number
}

export interface Fill {
  orderId: string
  intentId: string
  symbol: string
  /** Signed: a sell is negative. */
  qty: number
  price: number
  at: string
}

export interface OpenOrder {
  id: string
  intentId: string
  symbol: string
  side: string
  qty: number
  limit: number
  placedAt: string
}

export interface Book {
  mode: string
  startingCash: number
  cash: number
  equity: number
  openedAt: string
  positions: Position[]
  openOrders: OpenOrder[]
  fills: Fill[]
  history: { day: string; open: number; equity: number }[]
  gross: number
  grossOfEquity: number
}

export interface BarsInfo {
  symbol: string
  bars: number
  first?: string
  last?: string
  source?: string
  stale: boolean
  error?: string
}

/** One symbol's outcome from a bars refill. */
export interface RefillResult {
  symbol: string
  bars: number
  first?: string
  last?: string
  added: number
  error?: string
}

/** The dashboard in one round trip. */
export interface Overview {
  mode: Mode
  halted: boolean
  halt: Halt | null
  book: BookSummary | null
  bookError?: string
  strategies: Strategy[]
  lastRun: Run | null
  nextRun: { runId: string; at: string; earlyClose: boolean } | null
  limits: { dailyLossLimit: number; drawdownKillSwitch: number; maxOrdersPerDay: number }
}

/** What the machine has for running research from the app. */
export interface ResearchEnv {
  /** The research tree (the checkout's research/), and whether it is there. */
  dir: string
  tree: boolean
  /** The uv binary a job would use; empty until one is found or installed. */
  uv: string
  /** Whether the Python environment has been built at least once. */
  synced: boolean
  venv: string
  /** A job is running now. */
  busy: boolean
}

export interface Study {
  name: string
  title: string
  description: string
  script: string
  /** Set when the study takes a training-window end date. */
  defaultTrainTo?: string
}

export type JobStatus = 'running' | 'succeeded' | 'failed' | 'cancelled' | 'unknown'

export interface JobStep {
  name: string
  command: string
  status: JobStatus
  started: string
  ended?: string
}

export interface ResearchJob {
  id: string
  kind: 'setup' | 'run' | ''
  study?: string
  options: { trainTo?: string }
  status: JobStatus
  started: string
  ended?: string
  error?: string
  steps: JobStep[]
  /** What the study said about its spec, once it has. */
  outcome?: { promoted: boolean; line: string }
  logBytes: number
}

export interface ResearchInfo {
  env: ResearchEnv
  studies: Study[]
  current: ResearchJob | null
  recent: ResearchJob[]
  /** Every job with a log on disk, newest first. */
  logs: string[]
}

export interface JobLog {
  job: ResearchJob
  log: string
  next: number
}
