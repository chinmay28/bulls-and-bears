// Package risk is the gate every order passes through, identically in dry-run
// and live. Its rules are docs/PLAN.md §6; their thresholds come from
// risk.yaml, never from code. Guardrails are code, not prompts.
package risk

import (
	"context"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/broker"
	"github.com/chinmay28/bulls-and-bears/server/internal/marketdata"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
)

// Decision is the gate's answer for one intent. Every Decision is journalled
// before any order is sent, allowed or not.
type Decision struct {
	IntentID string
	Allowed  bool
	// Reason says why not, or how the intent was changed (scaled down).
	Reason string
	// Qty is the quantity allowed, which may be less than asked for when
	// gross leverage scales intents down.
	Qty float64
}

// State is everything the rules need to know about the account and the day.
type State struct {
	Now time.Time
	// Equity now, at the start of the day, and the high-water mark.
	Equity, StartOfDayEquity, HighWaterEquity float64
	Positions                                 []strategy.Position
	Quotes                                    []marketdata.Quote
	// OrdersToday is how many orders have already gone out today.
	OrdersToday int
	// DailyLimitHits is how many consecutive trading days the daily loss
	// limit has been hit.
	DailyLimitHits int
	// Live is true for the real broker; the confirmation threshold applies
	// only then.
	Live bool
}

// Gate checks intents against the rules and the state.
type Gate interface {
	Check(ctx context.Context, intents []broker.Order, state State) []Decision
}
