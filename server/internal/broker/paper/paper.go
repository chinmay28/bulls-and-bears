// Package paper is the broker that trades on paper.
//
// It fills orders against the latest quote — at the ask for a buy and the bid
// for a sell, moved against the trader by the spec's slippage and less the
// spec's commission — and keeps its book in SQLite so a restart picks up
// exactly where the last run stopped. Dry-run is this broker in place of the
// real one (docs/PLAN.md §1): the risk gate, the journal and the app see the
// same Broker either way and never learn which they are talking to.
//
// There is no order book. A market order and a marketable limit fill at once
// and in full; a limit that is not marketable rests as open until it is
// canceled, and is never filled later, because the book has no feed of its own
// to fill it from. That is a simplification the pairs strategy does not mind
// (it sends market orders at the close) and is written down here so nobody
// waits on a resting paper limit.
package paper

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/chinmay28/bulls-and-bears/server/internal/broker"
	"github.com/chinmay28/bulls-and-bears/server/internal/marketdata"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
)

// Costs is the spec's cost model (specs/strategy.schema.json, cost_model).
type Costs struct {
	// CommissionUSD is charged once per filled order.
	CommissionUSD float64
	// SlippageBps moves every fill against the trader: a buy pays
	// ask × (1 + bps/10_000), a sell receives bid × (1 − bps/10_000).
	SlippageBps float64
}

// Options configure a book. StartingCash is used only when the file is new;
// a reopened book keeps the cash it had.
type Options struct {
	StartingCash float64
	Costs        Costs
	// Clock is what stamps orders, fills and the equity history. Nil means
	// time.Now; tests inject a fixed one.
	Clock func() time.Time
}

// Order statuses as stored.
const (
	StatusFilled   = "filled"
	StatusOpen     = "open"
	StatusCanceled = "canceled"
	StatusRejected = "rejected"
)

// ErrRejected is wrapped by every Place error that is the order's own fault —
// no quote, bad quantity, not enough cash — as opposed to the database
// failing. The rejected order is on file with its reason either way.
var ErrRejected = errors.New("order rejected")

// Book is the paper account: one SQLite file, one mutex.
type Book struct {
	db     *sql.DB
	quotes marketdata.MarketData
	costs  Costs
	clock  func() time.Time
	// mu serializes everything that writes. SQLite would serialize the
	// statements anyway, but a Place is read-decide-write and two of them
	// interleaved could both pass the buying-power check on the same cash.
	mu sync.Mutex
}

// migrations are append-only. Never edit one that has shipped; add another.
var migrations = []string{
	// book is a singleton: the CHECK keeps a second row out.
	`CREATE TABLE book (
		id            INTEGER PRIMARY KEY CHECK (id = 1),
		cash          REAL NOT NULL,
		starting_cash REAL NOT NULL,
		opened_at     TEXT NOT NULL,
		next_order    INTEGER NOT NULL DEFAULT 1
	)`,
	`CREATE TABLE positions (
		symbol   TEXT PRIMARY KEY,
		qty      REAL NOT NULL,
		avg_cost REAL NOT NULL
	)`,
	// n is the counter the id was minted from, kept so orders sort in the
	// order they were placed without parsing the id.
	`CREATE TABLE orders (
		id          TEXT PRIMARY KEY,
		n           INTEGER NOT NULL UNIQUE,
		intent_id   TEXT NOT NULL,
		symbol      TEXT NOT NULL,
		side        TEXT NOT NULL,
		qty         REAL NOT NULL,
		limit_price REAL NOT NULL DEFAULT 0,
		status      TEXT NOT NULL,
		reason      TEXT NOT NULL DEFAULT '',
		placed_at   TEXT NOT NULL
	)`,
	`CREATE TABLE fills (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		order_id  TEXT NOT NULL REFERENCES orders(id),
		intent_id TEXT NOT NULL,
		symbol    TEXT NOT NULL,
		qty       REAL NOT NULL,
		price     REAL NOT NULL,
		at        TEXT NOT NULL
	)`,
	`CREATE INDEX idx_fills_at ON fills(at)`,
	// One row per UTC day. opening is the first equity seen that day and
	// answers the risk gate's start-of-day; equity is the latest, and is
	// what a chart plots.
	`CREATE TABLE equity_history (
		day     TEXT PRIMARY KEY,
		opening REAL NOT NULL,
		equity  REAL NOT NULL
	)`,
}

// timeLayout is fixed-width so that stored times compare as strings in the
// order they happened; RFC3339Nano trims zeros and would not.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

func stamp(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseStamp(s string) (time.Time, error) { return time.Parse(timeLayout, s) }

// Open creates or reopens the book at path. A new file starts with
// opts.StartingCash and records when it was opened; an existing one is left
// exactly as it was — cash, positions, fills, orders — and StartingCash is
// ignored, which is the whole point of persisting it.
func Open(path string, quotes marketdata.MarketData, opts Options) (*Book, error) {
	if quotes == nil {
		return nil, errors.New("paper: nil market data")
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("paper: create data dir: %w", err)
		}
	}
	// The file is created here, private, before SQLite touches it: SQLite
	// would make it 0644 less umask, and the -wal and -shm files it adds
	// later copy the database file's mode, so getting this one right covers
	// all three. An empty file is a valid empty database.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("paper: create book: %w", err)
	}
	f.Close()

	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("paper: open sqlite: %w", err)
	}
	// SQLite writes serialize anyway; a small pool keeps lock contention simple.
	db.SetMaxOpenConns(4)

	b := &Book{db: db, quotes: quotes, costs: opts.Costs, clock: opts.Clock}
	ctx := context.Background()
	if err := b.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := b.initBook(ctx, opts.StartingCash); err != nil {
		db.Close()
		return nil, err
	}
	return b, nil
}

// Close releases the file. The book is safe to reopen afterwards.
func (b *Book) Close() error { return b.db.Close() }

func (b *Book) migrate(ctx context.Context) error {
	if _, err := b.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		return fmt.Errorf("paper: create schema_migrations: %w", err)
	}
	var applied int
	if err := b.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&applied); err != nil {
		return fmt.Errorf("paper: read schema version: %w", err)
	}
	for i := applied; i < len(migrations); i++ {
		tx, err := b.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("paper: migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, i+1); err != nil {
			tx.Rollback()
			return fmt.Errorf("paper: record migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("paper: commit migration %d: %w", i+1, err)
		}
	}
	return nil
}

// initBook writes the singleton row on first open and leaves it alone after.
func (b *Book) initBook(ctx context.Context, startingCash float64) error {
	var n int
	if err := b.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM book`).Scan(&n); err != nil {
		return fmt.Errorf("paper: read book: %w", err)
	}
	if n > 0 {
		return nil
	}
	if startingCash <= 0 || math.IsNaN(startingCash) || math.IsInf(startingCash, 0) {
		return fmt.Errorf("paper: starting cash %v is not a positive amount", startingCash)
	}
	_, err := b.db.ExecContext(ctx,
		`INSERT INTO book (id, cash, starting_cash, opened_at, next_order) VALUES (1, ?, ?, ?, 1)`,
		startingCash, startingCash, stamp(b.clock()))
	if err != nil {
		return fmt.Errorf("paper: open book: %w", err)
	}
	return nil
}

// Place fills or rests one order. A market order fills at once at the slipped
// ask (buy) or bid (sell); a limit fills at once when it is marketable — buy
// limit at or above the ask, sell limit at or below the bid — at the better of
// the limit and the slipped market, and otherwise rests as open. Every order
// gets a row, a rejected one with its reason, and the id comes back only for
// an order that was accepted.
//
// A buy needs cash for the shares and the commission; there is no margin. A
// sell needs nothing: selling more than is held goes short, as the pairs
// strategy does with one leg.
func (b *Book) Place(ctx context.Context, o broker.Order) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock().UTC()

	// Shape first, then the market: an order that is wrong on its own terms
	// is rejected without asking for a quote.
	var q marketdata.Quote
	reason := ""
	switch {
	case o.Symbol == "":
		reason = "no symbol"
	case o.Side != broker.Buy && o.Side != broker.Sell:
		reason = fmt.Sprintf("side %d is neither buy nor sell", o.Side)
	case !(o.Qty > 0) || math.IsInf(o.Qty, 0):
		reason = fmt.Sprintf("qty %v is not positive", o.Qty)
	case o.Limit < 0 || math.IsNaN(o.Limit) || math.IsInf(o.Limit, 0):
		reason = fmt.Sprintf("limit %v is not a price", o.Limit)
	default:
		q, reason = b.quote(ctx, o.Symbol)
	}

	status := StatusFilled
	price := 0.0
	if reason == "" {
		price = slipped(q, o.Side, b.costs.SlippageBps)
		if o.Limit > 0 {
			switch {
			case o.Side == broker.Buy && o.Limit >= q.Ask:
				price = math.Min(o.Limit, price)
			case o.Side == broker.Sell && o.Limit <= q.Bid:
				price = math.Max(o.Limit, price)
			default:
				status = StatusOpen
				price = o.Limit
			}
		}
	}

	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var cash float64
	var n int64
	if err := tx.QueryRowContext(ctx, `SELECT cash, next_order FROM book WHERE id = 1`).Scan(&cash, &n); err != nil {
		return "", fmt.Errorf("paper: read book: %w", err)
	}
	// A resting buy is checked at its limit: the book has no margin, so an
	// order it could never pay for is refused now rather than left to rest.
	if reason == "" && o.Side == broker.Buy {
		if need := o.Qty*price + b.costs.CommissionUSD; cash < need {
			reason = fmt.Sprintf("buying power %.2f is short of %.2f for %v %s at %.4f plus %.2f commission",
				cash, need, o.Qty, o.Symbol, price, b.costs.CommissionUSD)
		}
	}
	if reason != "" {
		status = StatusRejected
	}

	id := fmt.Sprintf("paper-%d", n)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO orders (id, n, intent_id, symbol, side, qty, limit_price, status, reason, placed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, n, o.IntentID, o.Symbol, o.Side.String(), o.Qty, o.Limit, status, reason, stamp(now)); err != nil {
		return "", fmt.Errorf("paper: record order: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE book SET next_order = ? WHERE id = 1`, n+1); err != nil {
		return "", fmt.Errorf("paper: advance order counter: %w", err)
	}

	if status == StatusFilled {
		// Fill quantity is signed, like a position: a sell is a negative fill.
		delta := o.Qty
		if o.Side == broker.Sell {
			delta = -o.Qty
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO fills (order_id, intent_id, symbol, qty, price, at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, o.IntentID, o.Symbol, delta, price, stamp(now)); err != nil {
			return "", fmt.Errorf("paper: record fill: %w", err)
		}
		if err := applyToPosition(ctx, tx, o.Symbol, delta, price); err != nil {
			return "", err
		}
		// Realized P&L needs no separate line: cash out on the buy and cash
		// in on the sell leave exactly the profit or loss behind.
		cash -= delta*price + b.costs.CommissionUSD
		if _, err := tx.ExecContext(ctx, `UPDATE book SET cash = ? WHERE id = 1`, cash); err != nil {
			return "", fmt.Errorf("paper: update cash: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("paper: commit order: %w", err)
	}
	if status == StatusRejected {
		return "", fmt.Errorf("paper: %s: %w: %s", id, ErrRejected, reason)
	}
	return id, nil
}

// quote fetches one symbol's quote and says why it cannot be traded on, if
// it cannot. Both sides are required: a fill price is one of them.
func (b *Book) quote(ctx context.Context, symbol string) (marketdata.Quote, string) {
	qs, err := b.quotes.Quotes(ctx, []string{symbol})
	if err != nil {
		return marketdata.Quote{}, "no quote for " + symbol + ": " + err.Error()
	}
	for _, q := range qs {
		if q.Symbol != symbol {
			continue
		}
		if !(q.Bid > 0) || !(q.Ask > 0) {
			return q, fmt.Sprintf("quote for %s has no bid/ask (bid %v, ask %v)", symbol, q.Bid, q.Ask)
		}
		return q, ""
	}
	return marketdata.Quote{}, "no quote for " + symbol
}

// slipped is the market price a side pays after slippage.
func slipped(q marketdata.Quote, side broker.Side, bps float64) float64 {
	if side == broker.Buy {
		return q.Ask * (1 + bps/10_000)
	}
	return q.Bid * (1 - bps/10_000)
}

// applyToPosition folds a signed fill into the symbol's row.
func applyToPosition(ctx context.Context, tx *sql.Tx, symbol string, delta, price float64) error {
	var qty, avg float64
	err := tx.QueryRowContext(ctx, `SELECT qty, avg_cost FROM positions WHERE symbol = ?`, symbol).Scan(&qty, &avg)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("paper: read position: %w", err)
	}
	qty, avg = applyTrade(qty, avg, delta, price)
	if qty == 0 {
		_, err = tx.ExecContext(ctx, `DELETE FROM positions WHERE symbol = ?`, symbol)
	} else {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO positions (symbol, qty, avg_cost) VALUES (?, ?, ?)
			 ON CONFLICT(symbol) DO UPDATE SET qty = excluded.qty, avg_cost = excluded.avg_cost`,
			symbol, qty, avg)
	}
	if err != nil {
		return fmt.Errorf("paper: update position: %w", err)
	}
	return nil
}

// applyTrade is the average-cost arithmetic on a signed position: growing
// the position weights the new shares in, shrinking it leaves the average
// alone (the difference is realized), and flipping through zero starts a
// fresh position at the trade price. Pure, so it can be tabled.
func applyTrade(qty, avg, delta, price float64) (float64, float64) {
	next := qty + delta
	switch {
	case next == 0:
		return 0, 0
	case qty == 0 || (qty > 0) != (next > 0):
		return next, price
	case math.Abs(next) > math.Abs(qty):
		return next, (avg*math.Abs(qty) + price*math.Abs(delta)) / math.Abs(next)
	default:
		return next, avg
	}
}

// Cancel closes a resting limit. A filled, rejected, already-canceled or
// unknown order is an error: there is nothing left to cancel.
func (b *Book) Cancel(ctx context.Context, orderID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	res, err := b.db.ExecContext(ctx, `UPDATE orders SET status = ? WHERE id = ? AND status = ?`,
		StatusCanceled, orderID, StatusOpen)
	if err != nil {
		return fmt.Errorf("paper: cancel %s: %w", orderID, err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	var status string
	err = b.db.QueryRowContext(ctx, `SELECT status FROM orders WHERE id = ?`, orderID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("paper: cancel %s: no such order", orderID)
	}
	if err != nil {
		return fmt.Errorf("paper: cancel %s: %w", orderID, err)
	}
	return fmt.Errorf("paper: cancel %s: order is %s, not open", orderID, status)
}

// BuyingPower is cash. There is no margin on paper.
func (b *Book) BuyingPower(ctx context.Context) (float64, error) {
	var cash float64
	if err := b.db.QueryRowContext(ctx, `SELECT cash FROM book WHERE id = 1`).Scan(&cash); err != nil {
		return 0, fmt.Errorf("paper: read cash: %w", err)
	}
	return cash, nil
}

// Positions lists what is held, by symbol. Flat symbols have no row.
func (b *Book) Positions(ctx context.Context) ([]strategy.Position, error) {
	rows, err := b.db.QueryContext(ctx, `SELECT symbol, qty, avg_cost FROM positions WHERE qty != 0 ORDER BY symbol`)
	if err != nil {
		return nil, fmt.Errorf("paper: read positions: %w", err)
	}
	defer rows.Close()
	var out []strategy.Position
	for rows.Next() {
		var p strategy.Position
		if err := rows.Scan(&p.Symbol, &p.Qty, &p.AvgCost); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Fills lists executions at or after since, oldest first. Qty is signed: a
// sell is a negative fill, so a fill says its own direction.
func (b *Book) Fills(ctx context.Context, since time.Time) ([]broker.Fill, error) {
	rows, err := b.db.QueryContext(ctx,
		`SELECT order_id, intent_id, symbol, qty, price, at FROM fills WHERE at >= ? ORDER BY id`, stamp(since))
	if err != nil {
		return nil, fmt.Errorf("paper: read fills: %w", err)
	}
	defer rows.Close()
	var out []broker.Fill
	for rows.Next() {
		var f broker.Fill
		var at string
		if err := rows.Scan(&f.OrderID, &f.IntentID, &f.Symbol, &f.Qty, &f.Price, &at); err != nil {
			return nil, err
		}
		if f.At, err = parseStamp(at); err != nil {
			return nil, fmt.Errorf("paper: fill %s: %w", f.OrderID, err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Equity is cash plus every position at its mark, and is recorded in the
// equity history for the clock's day. A held symbol without a quote is an
// error, not a guess: the risk gate compares this number against limits.
func (b *Book) Equity(ctx context.Context) (float64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	m, err := b.mark(ctx)
	if err != nil {
		return 0, err
	}
	return m.equity, nil
}

// MarkedPosition is a position with the price it is marked at.
type MarkedPosition struct {
	Symbol  string
	Qty     float64
	AvgCost float64
	// Mark is the quote's Last, or the bid/ask mid when there is no Last.
	Mark          float64
	MarketValue   float64
	UnrealizedPnL float64
}

// OpenOrder is a resting limit.
type OpenOrder struct {
	ID       string
	IntentID string
	Symbol   string
	Side     broker.Side
	Qty      float64
	Limit    float64
	PlacedAt time.Time
}

// Snapshot is the book as the app shows it.
type Snapshot struct {
	StartingCash float64
	Cash         float64
	Equity       float64
	OpenedAt     time.Time
	Positions    []MarkedPosition
	OpenOrders   []OpenOrder
}

// DayEquity is one row of the equity history: the first and latest equity
// seen on a UTC day.
type DayEquity struct {
	Day    time.Time
	Open   float64
	Equity float64
}

// Snapshot marks the book and lists what rests. Like Equity it records the
// equity it computed, so a chart and the risk gate see the same number the
// app showed.
func (b *Book) Snapshot(ctx context.Context) (Snapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	m, err := b.mark(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	s := Snapshot{Cash: m.cash, Equity: m.equity, Positions: m.positions}
	var opened string
	if err := b.db.QueryRowContext(ctx, `SELECT starting_cash, opened_at FROM book WHERE id = 1`).Scan(&s.StartingCash, &opened); err != nil {
		return Snapshot{}, fmt.Errorf("paper: read book: %w", err)
	}
	if s.OpenedAt, err = parseStamp(opened); err != nil {
		return Snapshot{}, fmt.Errorf("paper: opened_at: %w", err)
	}
	rows, err := b.db.QueryContext(ctx,
		`SELECT id, intent_id, symbol, side, qty, limit_price, placed_at FROM orders WHERE status = ? ORDER BY n`, StatusOpen)
	if err != nil {
		return Snapshot{}, fmt.Errorf("paper: read open orders: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var o OpenOrder
		var side, placed string
		if err := rows.Scan(&o.ID, &o.IntentID, &o.Symbol, &side, &o.Qty, &o.Limit, &placed); err != nil {
			return Snapshot{}, err
		}
		if side == broker.Sell.String() {
			o.Side = broker.Sell
		} else {
			o.Side = broker.Buy
		}
		if o.PlacedAt, err = parseStamp(placed); err != nil {
			return Snapshot{}, fmt.Errorf("paper: order %s placed_at: %w", o.ID, err)
		}
		s.OpenOrders = append(s.OpenOrders, o)
	}
	return s, rows.Err()
}

// marked is what one pass over the quotes yields.
type marked struct {
	cash, equity float64
	positions    []MarkedPosition
}

// mark prices every position, sums the equity and writes it to the history.
// Callers hold mu.
func (b *Book) mark(ctx context.Context) (marked, error) {
	var m marked
	if err := b.db.QueryRowContext(ctx, `SELECT cash FROM book WHERE id = 1`).Scan(&m.cash); err != nil {
		return m, fmt.Errorf("paper: read cash: %w", err)
	}
	held, err := b.Positions(ctx)
	if err != nil {
		return m, err
	}
	m.equity = m.cash
	if len(held) > 0 {
		symbols := make([]string, len(held))
		for i, p := range held {
			symbols[i] = p.Symbol
		}
		qs, err := b.quotes.Quotes(ctx, symbols)
		if err != nil {
			return m, fmt.Errorf("paper: mark positions: %w", err)
		}
		bySymbol := make(map[string]marketdata.Quote, len(qs))
		for _, q := range qs {
			bySymbol[q.Symbol] = q
		}
		for _, p := range held {
			q, ok := bySymbol[p.Symbol]
			if !ok {
				return m, fmt.Errorf("paper: mark positions: no quote for held symbol %s", p.Symbol)
			}
			mark, ok := markPrice(q)
			if !ok {
				return m, fmt.Errorf("paper: mark positions: quote for %s has no last and no bid/ask", p.Symbol)
			}
			m.positions = append(m.positions, MarkedPosition{
				Symbol: p.Symbol, Qty: p.Qty, AvgCost: p.AvgCost,
				Mark:          mark,
				MarketValue:   p.Qty * mark,
				UnrealizedPnL: p.Qty * (mark - p.AvgCost),
			})
			m.equity += p.Qty * mark
		}
	}
	day := b.clock().UTC().Format("2006-01-02")
	if _, err := b.db.ExecContext(ctx,
		`INSERT INTO equity_history (day, opening, equity) VALUES (?, ?, ?)
		 ON CONFLICT(day) DO UPDATE SET equity = excluded.equity`, day, m.equity, m.equity); err != nil {
		return m, fmt.Errorf("paper: record equity: %w", err)
	}
	return m, nil
}

// markPrice is Last when the quote has one, else the mid. Neither is an error
// for the caller to raise.
func markPrice(q marketdata.Quote) (float64, bool) {
	if q.Last > 0 {
		return q.Last, true
	}
	if q.Bid > 0 && q.Ask > 0 {
		return (q.Bid + q.Ask) / 2, true
	}
	return 0, false
}

// EquityHistory lists every day the book was marked, oldest first.
func (b *Book) EquityHistory(ctx context.Context) ([]DayEquity, error) {
	rows, err := b.db.QueryContext(ctx, `SELECT day, opening, equity FROM equity_history ORDER BY day`)
	if err != nil {
		return nil, fmt.Errorf("paper: read equity history: %w", err)
	}
	defer rows.Close()
	var out []DayEquity
	for rows.Next() {
		var d DayEquity
		var day string
		if err := rows.Scan(&day, &d.Open, &d.Equity); err != nil {
			return nil, err
		}
		if d.Day, err = time.Parse("2006-01-02", day); err != nil {
			return nil, fmt.Errorf("paper: equity history day %q: %w", day, err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// StartOfDayEquity is what the book was worth when day began: the first mark
// of that day if there was one, else the last mark of any earlier day, else
// the starting cash. The risk gate's daily loss limit is measured from it.
func (b *Book) StartOfDayEquity(ctx context.Context, day time.Time) (float64, error) {
	d := day.UTC().Format("2006-01-02")
	var eq float64
	err := b.db.QueryRowContext(ctx, `SELECT opening FROM equity_history WHERE day = ?`, d).Scan(&eq)
	if err == nil {
		return eq, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("paper: read start of day: %w", err)
	}
	err = b.db.QueryRowContext(ctx, `SELECT equity FROM equity_history WHERE day < ? ORDER BY day DESC LIMIT 1`, d).Scan(&eq)
	if err == nil {
		return eq, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("paper: read start of day: %w", err)
	}
	if err := b.db.QueryRowContext(ctx, `SELECT starting_cash FROM book WHERE id = 1`).Scan(&eq); err != nil {
		return 0, fmt.Errorf("paper: read starting cash: %w", err)
	}
	return eq, nil
}

// HighWaterEquity is the most the book has ever been marked at, counting the
// cash it opened with. The drawdown kill switch is measured from it.
func (b *Book) HighWaterEquity(ctx context.Context) (float64, error) {
	var hw float64
	err := b.db.QueryRowContext(ctx, `SELECT MAX(v) FROM (
		SELECT starting_cash AS v FROM book
		UNION ALL SELECT opening FROM equity_history
		UNION ALL SELECT equity FROM equity_history)`).Scan(&hw)
	if err != nil {
		return 0, fmt.Errorf("paper: read high water: %w", err)
	}
	return hw, nil
}

// sortedSymbols is a small helper for deterministic quote requests in tests
// and logs.
func sortedSymbols(ps []strategy.Position) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Symbol
	}
	sort.Strings(out)
	return out
}

var _ broker.Broker = (*Book)(nil)
