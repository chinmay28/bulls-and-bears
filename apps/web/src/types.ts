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
  /** Where the spec may trade: paper until the operator promotes it to live. */
  stage: 'paper' | 'live'
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
  /** The spec strategy the study emits. */
  strategy: string
  /** Set when the study takes a training-window end date. */
  defaultTrainTo?: string
  /** The symbols the study runs on unless told otherwise, the haven last. */
  defaultUniverse: string[]
  /** How many symbols the study needs; absent for any number of two or more. */
  universeSize?: number
  universeHint: string
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
  options: { trainTo?: string; universe?: string[] }
  status: JobStatus
  started: string
  ended?: string
  error?: string
  steps: JobStep[]
  /** What the study said about its spec, once it has. */
  outcome?: { promoted: boolean; line: string }
  logBytes: number
}

/** One re-validation of one installed spec. */
export interface Revalidation {
  name: string
  study: string
  jobId?: string
  at: string
  status: JobStatus
  outcome?: { promoted: boolean; line: string }
  error?: string
}

/** The monthly re-validation of installed specs. */
export interface Schedule {
  enabled: boolean
  /** When each spec was last re-validated, by name. */
  lastRun: Record<string, string>
  /** Newest first. */
  history: Revalidation[]
  running: boolean
  /** The specs the next wake would re-run. */
  due: string[]
}

export interface ResearchInfo {
  env: ResearchEnv
  studies: Study[]
  current: ResearchJob | null
  recent: ResearchJob[]
  /** Every job with a log on disk, newest first. */
  logs: string[]
  schedule: Schedule | null
}

export interface JobLog {
  job: ResearchJob
  log: string
  next: number
}

/** The XLK/SATA rotation's lot, as the Rotation screen shows it. Read-only:
 *  the rotation is a separate process holding the broker connection and the
 *  data directory's lock, so the app watches it and can stop it, but cannot
 *  place an order for it. */
export interface Rotation {
  /** False when the rotation has never run in this data directory. */
  configured: boolean
  mode: 'flat' | 'held' | 'recovery' | string
  /** Whether a process still holds the lock, and which. */
  running: boolean
  holderPid: number

  /** The two symbols, so the screen names them rather than hard-coding them. */
  risk: string
  park: string

  entryPrice: number
  entryDate: string
  /** The original purchase value every percentage is measured against. */
  basis: number
  optionPnl: number
  dividends: number
  costs: number
  /** Everything the combined figure counts except the shares' own move. */
  banked: number

  quickTargetPct: number
  recoveryTargetPct: number
  recoveryTargetUsd: number
  /** The share price at which the combined figure reaches the target. */
  breakEvenPrice: number

  shortCall: RotationCall | null
  pending: RotationPending | null

  ordersToday: number
  startOfDayEquity: number
  highWaterEquity: number
  dailyLimitHits: number
  marksDay: string

  updatedAt: string
  /** Newest first, at most fifty. */
  history: RotationEntry[]
  schedule: RotationPhase[]
  rules: RotationRule[]
}

export interface RotationCall {
  optionId: string
  strike: number
  expiration: string
  credit: number
  /** What the lot returns if it is called away at this strike. */
  assignedPct: number
}

export interface RotationPending {
  orderId: string
  kind: string
  symbol: string
  strike: number
  placedAt: string
}

export interface RotationEntry {
  at: string
  mode: string
  entry: number
  optionPnl: number
  strike: number
  pending: string
}

export interface RotationPhase {
  name: string
  pt: string
  utc: string
  cron: string
  what: string
}

export interface RotationRule {
  label: string
  value: string
}
