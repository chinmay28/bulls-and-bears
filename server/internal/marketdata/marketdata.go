// Package marketdata is where quotes and history come from at run time.
//
// The interfaces are the plan's (docs/PLAN.md §5). Implementations live in
// subpackages: robinhood/ over the MCP client once Phase 3 lands, and a
// replay implementation for backtests that answers from Parquet bars.
package marketdata

import (
	"context"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
)

// Quote is the latest price of one symbol. At is the venue's timestamp, UTC;
// the risk gate rejects intents built on a quote older than its limit.
type Quote struct {
	Symbol         string
	Bid, Ask, Last float64
	At             time.Time
}

// MarketData answers the current quote for each symbol asked for.
type MarketData interface {
	Quotes(ctx context.Context, symbols []string) ([]Quote, error)
}

// HistoricalData answers daily bars. It may be unimplemented for Robinhood
// (docs/DISCOVERY.md, question 1), in which case Yahoo via the research side
// is the source.
type HistoricalData interface {
	Bars(ctx context.Context, symbol string, from, to time.Time) ([]bars.Bar, error)
}
