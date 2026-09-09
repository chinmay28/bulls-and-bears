// Package rotation runs the XLK/SATA rotation against a live broker: it
// reads the book into the strategy's snapshot, carries the strategy's plan
// back out as orders, folds the fills that come back into the lot's running
// account, and keeps all of it across restarts.
//
// The split is the plan's §1 in miniature.
// `internal/strategy/xlksata` decides and is pure; this package does all the
// I/O and decides nothing. Every rule about what may be traded lives there,
// so a bug here can fail to act, or act late, but cannot invent a trade the
// rules do not allow.
package rotation

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/xlksata"
)

// LedgerFile is where a data directory keeps the book: one timestamped JSON
// object per line, appended and never rewritten, exactly as internal/journal
// keeps a run.
//
// A line is a whole snapshot of the book rather than an event to fold,
// which is the choice worth explaining. The *why* of every change is already
// in the journal — the quote, the decision, the order — so an event log here
// would say the same things twice and put the burden of correctness on a
// replay. What this file has to survive instead is a crash, and a snapshot
// per line survives it in the way that matters: the last line may be torn,
// and the line before it is a complete and consistent book, one cycle stale.
// A fold over a torn event log loses the same cycle with more machinery.
//
// The history is the side benefit and it is a real one: every state the book
// has ever been in, timestamped, is still in the file.
func LedgerFile(dataDir string) string { return filepath.Join(dataDir, "rotation.jsonl") }

// Pending is an order that has been placed and not yet accounted for.
//
// It exists because a fill is what changes the lot's state, and a fill
// arrives after the cycle that caused it. Without this, the cycle after an
// entry fill would find 100 shares on a book whose state says flat and halt
// on the disagreement — correctly, since it would have no idea what they
// cost. The contract's terms are copied in at placement for the same reason:
// once the order has filled, what was written is what was intended, and a
// second lookup could answer differently.
type Pending struct {
	OrderID    string       `json:"order_id"`
	Kind       xlksata.Kind `json:"kind"`
	Symbol     string       `json:"symbol"`
	OptionID   string       `json:"option_id,omitempty"`
	Expiration time.Time    `json:"expiration,omitempty"`
	Strike     float64      `json:"strike,omitempty"`
	PlacedAt   time.Time    `json:"placed_at"`
}

// Marks are the day-scoped figures the risk gate reasons about, kept here
// because they have to survive a restart: a daemon that forgot how many
// orders it had sent today, or where the equity high-water mark was, would
// hand the gate a clean slate every time it came back up, which is exactly
// when the gate matters most.
type Marks struct {
	// Day is the trading date the day-scoped fields below belong to.
	Day              string  `json:"day"`
	StartOfDayEquity float64 `json:"start_of_day_equity"`
	HighWaterEquity  float64 `json:"high_water_equity"`
	OrdersToday      int     `json:"orders_today"`
	// LimitHitToday records that the daily loss limit was reached, so the
	// consecutive-days count can be rolled at the next day's first cycle.
	LimitHitToday bool `json:"limit_hit_today"`
	// DailyLimitHits is how many trading days in a row the limit was hit.
	DailyLimitHits int `json:"daily_limit_hits"`
}

// Roll brings the marks up to date for a trading day at the current equity.
// Within a day it only raises the high-water mark; across one it resets the
// day-scoped fields and carries the consecutive-limit count forward or
// clears it.
func (m Marks) Roll(day string, equity float64) Marks {
	if m.Day == day {
		if equity > m.HighWaterEquity {
			m.HighWaterEquity = equity
		}
		return m
	}
	next := Marks{
		Day:              day,
		StartOfDayEquity: equity,
		HighWaterEquity:  m.HighWaterEquity,
	}
	if equity > next.HighWaterEquity {
		next.HighWaterEquity = equity
	}
	// A first run has no previous day to judge.
	if m.Day != "" && m.LimitHitToday {
		next.DailyLimitHits = m.DailyLimitHits + 1
	}
	return next
}

// Book is everything this package persists: the strategy's state, the order
// it is waiting on, and the gate's running marks.
type Book struct {
	State   xlksata.State
	Pending *Pending
	Marks   Marks
	// At is when this book was written. Zero on a book that has never been
	// saved, which is a data directory that has never run.
	At time.Time
}

// line is the on-disk shape of one appended book.
type line struct {
	TS        time.Time      `json:"ts"`
	Version   int            `json:"version"`
	Mode      string         `json:"mode"`
	State     persistedState `json:"state"`
	ShortCall *persistedCall `json:"short_call,omitempty"`
	Pending   *Pending       `json:"pending,omitempty"`
	Marks     Marks          `json:"marks"`
}

type persistedState struct {
	EntryPrice float64   `json:"entry_price"`
	EntryDate  time.Time `json:"entry_date"`
	OptionPnL  float64   `json:"option_pnl"`
	Dividends  float64   `json:"dividends"`
	Costs      float64   `json:"costs"`
}

type persistedCall struct {
	OptionID   string    `json:"option_id"`
	Expiration time.Time `json:"expiration"`
	Strike     float64   `json:"strike"`
	Credit     float64   `json:"credit"`
}

const stateVersion = 1

func parseMode(s string) (xlksata.Mode, error) {
	switch s {
	case "flat", "":
		return xlksata.Flat, nil
	case "held":
		return xlksata.Held, nil
	case "recovery":
		return xlksata.Recovery, nil
	}
	return xlksata.Flat, fmt.Errorf("rotation: %q is not a mode", s)
}

func (l line) book() (Book, error) {
	if l.Version != stateVersion {
		return Book{}, fmt.Errorf("rotation: ledger line is version %d, this build writes %d", l.Version, stateVersion)
	}
	mode, err := parseMode(l.Mode)
	if err != nil {
		return Book{}, err
	}
	b := Book{
		State: xlksata.State{
			Mode:       mode,
			EntryPrice: l.State.EntryPrice,
			EntryDate:  l.State.EntryDate,
			OptionPnL:  l.State.OptionPnL,
			Dividends:  l.State.Dividends,
			Costs:      l.State.Costs,
		},
		Pending: l.Pending,
		Marks:   l.Marks,
		At:      l.TS,
	}
	if c := l.ShortCall; c != nil {
		b.State.ShortCall = &xlksata.ShortCall{
			OptionID: c.OptionID, Expiration: c.Expiration, Strike: c.Strike, Credit: c.Credit,
		}
	}
	if b.State.Open() && b.State.EntryPrice <= 0 {
		return Book{}, fmt.Errorf("rotation: ledger holds a %s lot with no entry price", b.State.Mode)
	}
	return b, nil
}

func toLine(b Book, now time.Time) line {
	l := line{
		TS:      now.UTC(),
		Version: stateVersion,
		Mode:    b.State.Mode.String(),
		State: persistedState{
			EntryPrice: b.State.EntryPrice,
			EntryDate:  b.State.EntryDate,
			OptionPnL:  b.State.OptionPnL,
			Dividends:  b.State.Dividends,
			Costs:      b.State.Costs,
		},
		Pending: b.Pending,
		Marks:   b.Marks,
	}
	if c := b.State.ShortCall; c != nil {
		l.ShortCall = &persistedCall{
			OptionID: c.OptionID, Expiration: c.Expiration, Strike: c.Strike, Credit: c.Credit,
		}
	}
	return l
}

// Load reads the book: the last line of the ledger that is a whole one.
//
// A missing file is a flat book with nothing pending, which is the correct
// reading of "this has never run". A torn final line is skipped — that is
// the crash this format is shaped to survive, and the line before it is a
// complete book one cycle old. A line that parses but does not make sense is
// an error and never a flat book: forgetting an open lot would open a second
// one on top of it.
func Load(dataDir string) (Book, error) {
	lines, err := readLines(dataDir)
	if err != nil {
		return Book{}, err
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var l line
		if err := json.Unmarshal([]byte(lines[i]), &l); err != nil {
			if i == len(lines)-1 {
				// Only the final line may be torn: a short write at the end
				// of an append is the one failure this format expects.
				continue
			}
			return Book{}, fmt.Errorf("rotation: %s line %d is not JSON: %w", LedgerFile(dataDir), i+1, err)
		}
		return l.book()
	}
	return Book{}, nil
}

// History reads every whole book the ledger holds, oldest first. It is for
// looking at what happened, not for deciding: nothing in the runtime reads
// more than the last line.
func History(dataDir string) ([]Book, error) {
	lines, err := readLines(dataDir)
	if err != nil {
		return nil, err
	}
	var out []Book
	for i, raw := range lines {
		var l line
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			if i == len(lines)-1 {
				continue
			}
			return nil, fmt.Errorf("rotation: %s line %d is not JSON: %w", LedgerFile(dataDir), i+1, err)
		}
		b, err := l.book()
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func readLines(dataDir string) ([]string, error) {
	f, err := os.Open(LedgerFile(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	// A book is small; the default 64 KiB token is already generous, but a
	// scanner that stops early would look exactly like a truncated file.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if s := strings.TrimSpace(sc.Text()); s != "" {
			lines = append(lines, s)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

// Save appends the book. Nothing is ever rewritten, so a crash can only ever
// lose the line being written, and the write is synced before it returns:
// the book has to be on disk before the order it authorises goes out.
func Save(dataDir string, b Book, now time.Time) error {
	raw, err := json.Marshal(toLine(b, now))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(LedgerFile(dataDir), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(raw, '\n')); err != nil {
		return err
	}
	return f.Sync()
}
