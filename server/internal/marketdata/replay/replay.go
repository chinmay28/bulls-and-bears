// Package replay answers quotes from the newest bar on disk.
//
// It is the market-data source for a dry-run that has no live feed yet: the
// Robinhood adapter arrives with Phase 3, and until then the paper book needs
// a price to fill at and the risk gate a quote to judge. The bid and ask are
// the close spread by a configurable half-width, Last is the close, and the
// quote is stamped with the clock's now — the bar is the freshest thing this
// source knows, and it says so rather than pretending to be a venue. The
// bars' own staleness is checked separately by the runner.
package replay

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/marketdata"
)

// Source serves quotes from <Dir>/<SYMBOL>.parquet.
type Source struct {
	Dir string
	// SpreadBps is the full bid-ask spread as basis points of the close;
	// zero means bid = ask = close.
	SpreadBps float64
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	loaded time.Time
	last   bars.Bar
}

// Quotes answers one quote per symbol, in the order asked. A symbol with no
// file, or an empty one, is an error: the caller must not guess a price.
func (s *Source) Quotes(_ context.Context, symbols []string) ([]marketdata.Quote, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	out := make([]marketdata.Quote, 0, len(symbols))
	for _, sym := range symbols {
		last, err := s.last(sym, now())
		if err != nil {
			return nil, err
		}
		half := last.Close * s.SpreadBps / 20_000
		out = append(out, marketdata.Quote{
			Symbol: sym,
			Bid:    last.Close - half,
			Ask:    last.Close + half,
			Last:   last.Close,
			At:     now(),
		})
	}
	return out, nil
}

// last reads the newest bar of a symbol, re-reading the file at most once a
// minute so a run that asks for the same quote several times does not parse
// the whole series each time.
func (s *Source) last(symbol string, now time.Time) (bars.Bar, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.cache[symbol]; ok && now.Sub(c.loaded) < time.Minute {
		return c.last, nil
	}
	series, err := bars.Read(filepath.Join(s.Dir, symbol+".parquet"))
	if err != nil {
		return bars.Bar{}, fmt.Errorf("replay: %w", err)
	}
	if len(series) == 0 {
		return bars.Bar{}, fmt.Errorf("replay: %s has no bars", symbol)
	}
	if s.cache == nil {
		s.cache = map[string]cached{}
	}
	s.cache[symbol] = cached{loaded: now, last: series[len(series)-1]}
	return series[len(series)-1], nil
}

var _ marketdata.MarketData = (*Source)(nil)
