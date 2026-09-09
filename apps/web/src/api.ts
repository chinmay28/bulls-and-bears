import type {
  Backtest,
  BarsInfo,
  Book,
  HaltStatus,
  JobLog,
  Overview,
  RefillResult,
  ResearchInfo,
  ResearchJob,
  Rotation,
  Run,
  Schedule,
  RunDetail,
  SelfInfo,
  Strategy,
} from './types'

/** ApiError carries the server's message so the UI can show it verbatim. */
export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let response: Response
  try {
    response = await fetch(path, {
      ...init,
      headers: init?.body ? { 'Content-Type': 'application/json', ...init?.headers } : init?.headers,
    })
  } catch {
    throw new ApiError(0, 'Could not reach Bulls and Bears. Is it still running?')
  }
  if (response.status === 204) return undefined as T
  const text = await response.text()
  const body = text ? JSON.parse(text) : undefined
  if (!response.ok) {
    throw new ApiError(response.status, body?.error ?? `Request failed (${response.status})`)
  }
  return body as T
}

const json = (body: unknown): RequestInit => ({ body: JSON.stringify(body) })

export const api = {
  session: () => request<{ required: boolean; authenticated: boolean }>('/api/session'),
  login: (pin: string) => request<{ authenticated: boolean }>('/api/session', { method: 'POST', ...json({ pin }) }),

  self: () => request<SelfInfo>('/api/self'),
  overview: () => request<Overview>('/api/overview'),

  /** Stop trading. The marker stays until resume, whatever else happens. */
  halt: (reason: string) => request<HaltStatus>('/api/halt', { method: 'POST', ...json({ reason }) }),
  resume: () => request<HaltStatus>('/api/halt', { method: 'DELETE' }),

  strategies: () => request<Strategy[]>('/api/strategies'),
  strategy: (name: string) => request<Strategy>(`/api/strategies/${encodeURIComponent(name)}`),
  /** Replays the spec over the bars on disk from its test window; the same
   *  run `bnb backtest` does. */
  backtest: (name: string) =>
    request<Backtest>(`/api/strategies/${encodeURIComponent(name)}/backtest`, { method: 'POST' }),
  bars: () => request<BarsInfo[]>('/api/bars'),
  /** Installs a spec research wrote. The server applies the same schema, the
   *  same out-of-sample floor and the same TTL a run applies, so this cannot
   *  install a strategy the runtime would only refuse. */
  importStrategy: (yaml: string, replace = false) =>
    request<Strategy>('/api/strategies', { method: 'POST', ...json({ yaml, replace }) }),
  /** Removes a spec. The bars stay: they cost a fetch and belong to no one spec. */
  deleteStrategy: (name: string) =>
    request<void>(`/api/strategies/${encodeURIComponent(name)}`, { method: 'DELETE' }),
  /** Refills the bars on disk from Yahoo. With no symbols it does every one
   *  the specs name and every one already on disk. */
  refreshBars: (symbols?: string[]) =>
    request<RefillResult[]>('/api/bars/refresh', { method: 'POST', ...json({ symbols: symbols ?? [] }) }),

  book: () => request<Book>('/api/book'),

  research: () => request<ResearchInfo>('/api/research'),
  /** Finds or installs uv and builds the Python environment; returns at once. */
  researchSetup: () => request<ResearchJob>('/api/research/setup', { method: 'POST' }),
  /** Runs a study with its output pointed at this machine's specs and bars.
   *  The spec is promoted only if the study's own gate passes it. */
  researchRun: (study: string, trainTo?: string, universe?: string[]) =>
    request<ResearchJob>('/api/research/runs', {
      method: 'POST',
      ...json({ study, trainTo: trainTo ?? '', universe: universe ?? [] }),
    }),
  /** The job and its log from a byte offset, so polling only carries what is new. */
  researchJob: (id: string, from = 0) =>
    request<JobLog>(`/api/research/jobs/${encodeURIComponent(id)}?from=${from}`),
  researchCancel: (id: string) =>
    request<ResearchJob>(`/api/research/jobs/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  /** Turns the monthly re-validation of installed specs on or off. */
  setSchedule: (enabled: boolean) => request<Schedule>('/api/research/schedule', { method: 'PUT', ...json({ enabled }) }),
  /** Re-validates every installed spec now, in the background. */
  revalidateNow: () => request<Schedule>('/api/research/schedule/run', { method: 'POST' }),
  /** Promotes a spec to live, or returns it to paper. The operator's call,
   *  kept outside the spec so research cannot make it. */
  setStage: (name: string, stage: 'paper' | 'live') =>
    request<Strategy>(`/api/strategies/${encodeURIComponent(name)}/stage`, { method: 'PUT', ...json({ stage }) }),

  /** The rotation's lot. Read-only, and it never takes the rotation's lock:
   *  a status card that could block a trading cycle would be worse than none. */
  rotation: () => request<Rotation>('/api/rotation'),

  runs: () => request<Run[]>('/api/runs'),
  run: (id: string) => request<RunDetail>(`/api/runs/${encodeURIComponent(id)}`),
  /** Today's cycle, now. Harmless in dry-run; a trading decision in live. */
  runNow: () => request<{ run: Run }>('/api/run', { method: 'POST' }),
}
