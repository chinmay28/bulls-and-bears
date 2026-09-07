import { useEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { Link, NavLink, useNavigate } from 'react-router-dom'
import { APP_VERSION } from '../version'

/** How long a badge thrown over the app stays on screen. Kept in sync with
 * the `dev-flash*` animations in styles.css — the CSS fades out on its own
 * clock, this unmounts it afterwards. */
const FLASH_MS = 3000

/** Two taps closer together than this are one double tap. Browsers use about
 * 300ms for their own; a little more forgives a thumb. */
const DOUBLE_TAP_MS = 400

/** A flash is a badge shown over the whole app for a moment: on until FLASH_MS
 * runs out, a tap on it, or Escape. Both marks in the header have one. */
function useFlash(): [boolean, () => void, () => void] {
  const [flash, setFlash] = useState(false)

  useEffect(() => {
    if (!flash) return

    const timer = window.setTimeout(() => setFlash(false), FLASH_MS)
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setFlash(false)
    }

    window.addEventListener('keydown', onKey)
    return () => {
      window.clearTimeout(timer)
      window.removeEventListener('keydown', onKey)
    }
  }, [flash])

  return [flash, () => setFlash(true), () => setFlash(false)]
}

/** The veil and lockup a flash is shown in. Portalled to the body on purpose:
 * the header carries a backdrop-filter of its own, and an element with one
 * becomes the containing block for fixed descendants — nested there the
 * overlay blurred nothing and grew out of the header instead of the middle of
 * the screen. */
function Flash({ onDismiss, children }: { onDismiss: () => void; children: ReactNode }) {
  return createPortal(
    <div className="dev-flash" role="presentation" onClick={onDismiss}>
      <div className="dev-flash-lockup">{children}</div>
    </div>,
    document.body,
  )
}

/** The header the four tabs share. App renders it once, outside the routes, so
 * moving between tabs leaves the brand lockup and the developer mark exactly
 * where they were. Which tab you are on is the tab bar's job to say; the
 * header's job is to stay put and carry identity only. */
export function AppHeader() {
  return (
    <header className="header">
      <BrandMark />
      {/* Name over version, as a lockup — the version reads as part of the
          name rather than as another thing on the screen. */}
      <div className="brand">
        <h1>Bulls and Bears</h1>
        <span className="brand-version">{APP_VERSION}</span>
      </div>
      <DevMark />
    </header>
  )
}

/** A floating action button above the tab bar, in the corner the thumb
 * already rests in. */
export function Fab({ to, label }: { to: string; label: string }) {
  return (
    <Link className="fab" to={to} aria-label={label} title={label}>
      <svg viewBox="0 0 24 24" aria-hidden="true" {...stroke} strokeWidth={2.2}>
        <path d="M12 5v14M5 12h14" />
      </svg>
    </Link>
  )
}

/** The body of a tab root. Its header belongs to the shell, so a tab supplies
 * content only. */
export function TabPage({ children }: { children: ReactNode }) {
  return <main className="main">{children}</main>
}

/** Page gives a pushed screen — a strategy, a run — its own sticky header:
 * the way back, what you tapped into, and its action. */
export function Page({
  title,
  back,
  action,
  children,
}: {
  title: string
  /** Path to go back to; omitted where there is nothing to go back to. */
  back?: string
  action?: ReactNode
  children: ReactNode
}) {
  const navigate = useNavigate()
  return (
    <>
      <header className="header">
        {back && (
          <button className="back" onClick={() => navigate(back)} aria-label="Back">
            ‹ Back
          </button>
        )}
        <h1>{title}</h1>
        {action}
      </header>
      <main className="main">{children}</main>
    </>
  )
}

/** The app's own mark at the start of the header. Double-tapping it throws the
 * whole logo over the app, the way the developer mark does on one tap. Two
 * taps rather than one because this corner is where a thumb lands while
 * reaching for the first card. */
function BrandMark() {
  const [flash, show, hide] = useFlash()
  const lastTap = useRef(0)

  const onClick = () => {
    const now = performance.now()
    if (now - lastTap.current < DOUBLE_TAP_MS) {
      lastTap.current = 0
      show()
    } else {
      lastTap.current = now
    }
  }

  return (
    <>
      <button
        type="button"
        className="brand-mark"
        title="Bulls and Bears"
        aria-label="Show the Bulls and Bears logo (double tap)"
        onClick={onClick}
      >
        <img className="brand-logo" src="/icon-192.png" alt="" aria-hidden="true" />
      </button>

      {flash && (
        <Flash onDismiss={hide}>
          <img className="brand-flash-logo" src="/logo.png" alt="Bulls and Bears" />
          <span className="dev-flash-handle">The agentic trading toolkit</span>
        </Flash>
      )}
    </>
  )
}

/** The developer mark at the end of the shared header: a small dark disk that
 * throws the full badge over the app for a moment when it is tapped.
 * Deliberately a button rather than a link — it goes nowhere. */
function DevMark() {
  const [flash, show, hide] = useFlash()

  return (
    <>
      <button
        type="button"
        className="dev"
        title="Built by CM Hegday · 0x434d"
        aria-label="Show the developer badge"
        onClick={show}
      >
        <img className="dev-logo" src="/dev-badge.png" alt="" aria-hidden="true" />
      </button>

      {flash && (
        <Flash onDismiss={hide}>
          <img className="dev-flash-logo" src="/dev-badge-full.png" alt="Built by CM Hegday — 0x434d" />
          <span className="dev-flash-handle">github.com/chinmay28</span>
        </Flash>
      )}
    </>
  )
}

export function TabBar() {
  return (
    <nav className="tabbar" aria-label="Sections">
      <NavLink to="/" end>
        <HomeIcon />
        Overview
      </NavLink>
      <NavLink to="/strategies">
        <StrategiesIcon />
        Strategies
      </NavLink>
      <NavLink to="/book">
        <BookIcon />
        Book
      </NavLink>
      <NavLink to="/settings">
        <GearIcon />
        Settings
      </NavLink>
    </nav>
  )
}

const stroke = {
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 1.8,
  strokeLinecap: 'round' as const,
  strokeLinejoin: 'round' as const,
}

function HomeIcon() {
  return (
    <svg viewBox="0 0 24 24" {...stroke}>
      <path d="M3 10.5 12 3l9 7.5" />
      <path d="M5.5 9.5V20h13V9.5" />
    </svg>
  )
}

/** A price path: the thing a strategy reads. */
function StrategiesIcon() {
  return (
    <svg viewBox="0 0 24 24" {...stroke}>
      <path d="M3 17l5-6 4 4 4-7 5 5" />
      <path d="M3 21h18" />
    </svg>
  )
}

/** A ledger: positions and fills. */
function BookIcon() {
  return (
    <svg viewBox="0 0 24 24" {...stroke}>
      <path d="M5 4h11a3 3 0 0 1 3 3v13H8a3 3 0 0 0-3 3z" />
      <path d="M5 4v16a3 3 0 0 1 3-3h11" />
      <path d="M9 9h6M9 13h4" />
    </svg>
  )
}

function GearIcon() {
  return (
    <svg viewBox="0 0 24 24" {...stroke}>
      <circle cx="12" cy="12" r="3.2" />
      <path d="M19.4 15a1.6 1.6 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.6 1.6 0 0 0-1.8-.3 1.6 1.6 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1A1.6 1.6 0 0 0 9 19.4a1.6 1.6 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.6 1.6 0 0 0 .3-1.8 1.6 1.6 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1A1.6 1.6 0 0 0 4.6 9a1.6 1.6 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.6 1.6 0 0 0 1.8.3H9a1.6 1.6 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.6 1.6 0 0 0 1 1.5 1.6 1.6 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.6 1.6 0 0 0-.3 1.8V9a1.6 1.6 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.6 1.6 0 0 0-1.5 1z" />
    </svg>
  )
}
