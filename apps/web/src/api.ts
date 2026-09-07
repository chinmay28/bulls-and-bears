import type { Backtest, BarsInfo, Book, HaltStatus, Overview, Run, RunDetail, SelfInfo, Strategy } from './types'

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

  book: () => request<Book>('/api/book'),

  runs: () => request<Run[]>('/api/runs'),
  run: (id: string) => request<RunDetail>(`/api/runs/${encodeURIComponent(id)}`),
  /** Today's cycle, now. Harmless in dry-run; a trading decision in live. */
  runNow: () => request<{ run: Run }>('/api/run', { method: 'POST' }),
}
