import { useEffect, useState, type FormEvent } from 'react'
import { Route, Routes, useLocation } from 'react-router-dom'
import { api, ApiError } from './api'
import { AppHeader, TabBar } from './components/Layout'
import { Banner } from './components/ui'
import Book from './pages/Book'
import NotFound from './pages/NotFound'
import Overview from './pages/Overview'
import RunDetail from './pages/RunDetail'
import Settings from './pages/Settings'
import Strategies from './pages/Strategies'
import StrategyDetail from './pages/StrategyDetail'

/** The four tab roots. The header is rendered outside the routes so moving
 * between tabs leaves it untouched; pushed screens bring their own. */
const TABS = new Set(['/', '/strategies', '/book', '/settings'])

export default function App() {
  const { pathname } = useLocation()
  const onTab = TABS.has(pathname)
  const [locked, setLocked] = useState<boolean | null>(null)

  // A PIN, when one is set, gates the API and not the page: the app loads and
  // asks for it here.
  useEffect(() => {
    api
      .session()
      .then((s) => setLocked(s.required && !s.authenticated))
      .catch(() => setLocked(false))
  }, [])

  if (locked === null) return null
  if (locked) return <PinScreen onUnlocked={() => setLocked(false)} />

  return (
    <div className="app">
      {onTab && <AppHeader />}
      <Routes>
        <Route path="/" element={<Overview />} />
        <Route path="/strategies" element={<Strategies />} />
        <Route path="/strategies/:name" element={<StrategyDetail />} />
        <Route path="/book" element={<Book />} />
        <Route path="/runs/:id" element={<RunDetail />} />
        <Route path="/settings" element={<Settings />} />
        <Route path="*" element={<NotFound />} />
      </Routes>
      <TabBar />
    </div>
  )
}

/** The lock screen. Nothing else renders until the PIN is accepted, so the
 * session cookie it earns is in place before the first real request. */
function PinScreen({ onUnlocked }: { onUnlocked: () => void }) {
  const [pin, setPin] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await api.login(pin)
      onUnlocked()
    } catch (err) {
      setError(err instanceof ApiError && err.status === 401 ? 'That is not the PIN.' : String(err))
      setPin('')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="app">
      <main className="main lock">
        <img className="lock-logo" src="/icon-192.png" alt="" />
        <h1>Bulls and Bears</h1>
        <form onSubmit={submit}>
          {error && <Banner tone="bad">{error}</Banner>}
          <input
            type="password"
            inputMode="numeric"
            autoComplete="current-password"
            placeholder="PIN"
            value={pin}
            onChange={(e) => setPin(e.target.value)}
            autoFocus
          />
          <button className="primary block" type="submit" disabled={busy || pin === ''}>
            {busy ? 'Checking…' : 'Unlock'}
          </button>
        </form>
      </main>
    </div>
  )
}
