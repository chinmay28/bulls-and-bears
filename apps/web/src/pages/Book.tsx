import { Link } from 'react-router-dom'
import { api, ApiError } from '../api'
import { TabPage } from '../components/Layout'
import { EquityLine } from '../components/signal'
import { ModeBadge } from '../components/status'
import { Badge, Card, Empty, Loading, Meter, SectionTitle, useLoader } from '../components/ui'
import { parts, pct, shares, time, usd } from '../lib/format'
import type { Book as BookData } from '../types'

/** Positions, working orders and fills — the paper book, and later the live
 *  one beside it. */
export default function Book() {
  const { data, error, loading, offline } = useLoader(() => api.book(), [], 10000)
  const noBook = error && !offline && !data && (error as unknown) instanceof ApiError

  return (
    <TabPage>
      {!noBook && <Loading error={error} offline={offline} hasData={!!data} />}
      {!data && loading && <div className="empty">Loading…</div>}
      {noBook && <Empty message={error ?? ''} />}
      <RotationCard />
      {data && <BookView book={data} />}
    </TabPage>
  )
}

/** The way into the rotation, with enough on it to not need the tap: the
 *  rotation is a separate runtime from the paper book, and this is where an
 *  account is looked at. Its own errors stay on it — a rotation that has
 *  never run must not make the Book tab look broken. */
function RotationCard() {
  const { data } = useLoader(() => api.rotation(), [], 30000)
  if (!data) return null
  const open = data.mode === 'held' || data.mode === 'recovery'
  return (
    <Link to="/rotation" style={{ textDecoration: 'none', color: 'inherit' }}>
      <Card>
        <div className="row between">
          <div className="grow">
            <div className="title">
              {data.risk} / {data.park} rotation
            </div>
            <div className="sub">
              {parts(
                data.mode,
                data.running ? 'running' : data.configured ? 'not running' : 'never run here',
                open && `entry ${usd(data.entryPrice)}`,
                open && `banked ${usd(data.banked, true)} of ${usd(data.recoveryTargetUsd)}`,
                data.pending ? 'waiting on an order' : false,
              )}
            </div>
          </div>
          <Badge tone={data.mode === 'recovery' ? 'warn' : open ? 'good' : 'neutral'}>{data.mode}</Badge>
        </div>
      </Card>
    </Link>
  )
}

function BookView({ book }: { book: BookData }) {
  const since = book.startingCash > 0 ? book.equity / book.startingCash - 1 : 0
  return (
    <>
      <Card>
        <div className="row between">
          <div className="grow">
            <div className="title">Paper book</div>
            <div className="sub">{parts(`opened ${time(book.openedAt)}`, `cash ${usd(book.cash)}`)}</div>
          </div>
          <ModeBadge mode={book.mode} />
        </div>
        <div className="equity">{usd(book.equity)}</div>
        <div className="sub">
          <span style={{ color: since >= 0 ? 'var(--good)' : 'var(--bad)', fontWeight: 600 }}>{pct(since, true)}</span> since{' '}
          {usd(book.startingCash)}
        </div>
        <EquityLine values={book.history.map((d) => d.equity)} />
        <div style={{ marginTop: 10 }}>
          <Meter label="Gross exposure" value={book.grossOfEquity * 100} display={`${usd(book.gross)} · ${pct(book.grossOfEquity)} of equity`} />
        </div>
      </Card>

      <SectionTitle>Positions</SectionTitle>
      {book.positions.length === 0 ? (
        <Card>
          <div className="empty" style={{ padding: 14 }}>
            Flat. Positions appear here once a run places something.
          </div>
        </Card>
      ) : (
        <Card>
          {book.positions.map((p, i) => (
            <div key={p.symbol}>
              {i > 0 && <div className="list-divider inset" />}
              <div className="row between" style={{ minHeight: 44 }}>
                <div className="grow">
                  <div className="title">
                    {p.symbol} <span className="sub">{p.qty >= 0 ? 'long' : 'short'} {shares(p.qty)} sh</span>
                  </div>
                  <div className="sub">
                    avg {usd(p.avgCost)} · mark {usd(p.mark)}
                  </div>
                </div>
                <span className="num" style={{ fontWeight: 600, color: p.unrealizedPnl >= 0 ? 'var(--good)' : 'var(--bad)' }}>
                  {usd(p.unrealizedPnl, true)}
                </span>
              </div>
            </div>
          ))}
        </Card>
      )}

      <SectionTitle>Working orders</SectionTitle>
      <Card>
        {book.openOrders.length === 0 ? (
          <div className="empty" style={{ padding: 14 }}>
            Nothing working. Orders go out at the close run and fill against the quote.
          </div>
        ) : (
          book.openOrders.map((o, i) => (
            <div key={o.id}>
              {i > 0 && <div className="list-divider inset" />}
              <div className="row between" style={{ minHeight: 44 }}>
                <div className="grow">
                  <div className="title">
                    {o.side.toUpperCase()} {o.symbol} {shares(o.qty)} @ {usd(o.limit)}
                  </div>
                  <div className="sub">
                    {o.id} · {time(o.placedAt)}
                  </div>
                </div>
                <Badge tone="accent">Open</Badge>
              </div>
            </div>
          ))
        )}
      </Card>

      <SectionTitle>Fills</SectionTitle>
      <Card>
        {book.fills.length === 0 ? (
          <div className="empty" style={{ padding: 14 }}>
            No fills yet.
          </div>
        ) : (
          book.fills.map((f, i) => (
            <div key={`${f.orderId}-${i}`}>
              {i > 0 && <div className="list-divider inset" />}
              <div className="row between" style={{ minHeight: 44 }}>
                <div className="grow">
                  <div className="title">
                    <span style={{ color: f.qty >= 0 ? 'var(--good)' : 'var(--bad)' }}>{f.qty >= 0 ? 'BUY' : 'SELL'}</span> {f.symbol}{' '}
                    {shares(f.qty)} @ {usd(f.price)}
                  </div>
                  <div className="sub">
                    {time(f.at)} · {f.orderId}
                  </div>
                </div>
                <Badge tone="neutral">Paper</Badge>
              </div>
            </div>
          ))
        )}
      </Card>

      <SectionTitle>Paper vs live</SectionTitle>
      <Card>
        <div className="row between">
          <div className="grow">
            <div className="title">Live account not trading</div>
            <div className="sub">Paper fills use bid/ask plus 5 bps. When live runs beside it, this compares the two.</div>
          </div>
          <Badge tone="neutral">Paper only</Badge>
        </div>
      </Card>
    </>
  )
}
