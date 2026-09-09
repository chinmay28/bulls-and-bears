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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/xlksata"
)

// StateFile is where a data directory keeps the open lot.
func StateFile(dataDir string) string { return filepath.Join(dataDir, "rotation.json") }

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
	if m.Day != "" {
		if m.LimitHitToday {
			next.DailyLimitHits = m.DailyLimitHits + 1
		}
	}
	return next
}

// Book is everything this package persists: the strategy's state, the order
// it is waiting on, and the gate's running marks.
type Book struct {
	State   xlksata.State
	Pending *Pending
	Marks   Marks
}

type stored struct {
	Version   int            `json:"version"`
	SavedAt   time.Time      `json:"saved_at"`
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

// Load reads the book. A missing file is a flat book with nothing pending,
// which is the correct reading of "this has never run". An unreadable or
// unrecognised one is an error and never a flat book: forgetting an open lot
// would open a second one on top of it.
func Load(dataDir string) (Book, error) {
	raw, err := os.ReadFile(StateFile(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return Book{}, nil
	}
	if err != nil {
		return Book{}, err
	}
	var s stored
	if err := json.Unmarshal(raw, &s); err != nil {
		return Book{}, fmt.Errorf("rotation: %s: %w", StateFile(dataDir), err)
	}
	if s.Version != stateVersion {
		return Book{}, fmt.Errorf("rotation: %s is version %d, this build writes %d",
			StateFile(dataDir), s.Version, stateVersion)
	}
	mode, err := parseMode(s.Mode)
	if err != nil {
		return Book{}, fmt.Errorf("rotation: %s: %w", StateFile(dataDir), err)
	}
	b := Book{
		State: xlksata.State{
			Mode:       mode,
			EntryPrice: s.State.EntryPrice,
			EntryDate:  s.State.EntryDate,
			OptionPnL:  s.State.OptionPnL,
			Dividends:  s.State.Dividends,
			Costs:      s.State.Costs,
		},
		Pending: s.Pending,
		Marks:   s.Marks,
	}
	if c := s.ShortCall; c != nil {
		b.State.ShortCall = &xlksata.ShortCall{
			OptionID: c.OptionID, Expiration: c.Expiration, Strike: c.Strike, Credit: c.Credit,
		}
	}
	if b.State.Open() && b.State.EntryPrice <= 0 {
		return Book{}, fmt.Errorf("rotation: %s holds a %s lot with no entry price",
			StateFile(dataDir), b.State.Mode)
	}
	return b, nil
}

// Save writes the book, atomically, 0600. The rename is what makes a crash
// mid-write leave the previous book rather than half of this one.
func Save(dataDir string, b Book, now time.Time) error {
	s := stored{
		Version: stateVersion,
		SavedAt: now.UTC(),
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
		s.ShortCall = &persistedCall{
			OptionID: c.OptionID, Expiration: c.Expiration, Strike: c.Strike, Credit: c.Credit,
		}
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	tmp := StateFile(dataDir) + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, StateFile(dataDir))
}
