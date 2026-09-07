// Package refill brings a bars directory up to date from a fetcher, so the
// operator can refill history from the phone instead of re-running the
// research side at a terminal.
//
// It owns one thing: for each symbol, decide the range to ask for, ask, and
// write the answer to <dir>/<SYMBOL>.parquet. It does not know where bars
// come from (the Fetcher does), what a strategy needs, or whether the result
// is fresh enough to trade — the runner decides that at run time, unchanged.
//
// It fails closed in the way that matters for data on disk: a symbol whose
// fetch errors, or whose answer holds fewer bars than the file already has,
// is left exactly as it was and reported. A bad afternoon at Yahoo must not
// be able to erase history the runtime is about to trade on.
package refill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
)

// Fetcher is where bars come from. yahoo.Client implements it; a test's stub
// does too, which is the point.
type Fetcher interface {
	Daily(ctx context.Context, symbol string, from, to time.Time) ([]bars.Bar, error)
}

// DefaultLookback is how much history a symbol with no file yet is given:
// enough for a lookback window and a train/test split, and small enough that
// one phone tap is not a raid on Yahoo.
const DefaultLookback = 6 * 365 * 24 * time.Hour

// safeSymbol is the universe pattern from specs/strategy.schema.json. A
// symbol becomes a filename here, so it is checked before it reaches
// filepath.Join, whoever asked for it.
var safeSymbol = regexp.MustCompile(`^[A-Z][A-Z0-9.]{0,9}$`)

// Options say where the files live and how far back to reach.
type Options struct {
	// Dir holds <SYMBOL>.parquet. Created if missing.
	Dir string
	// Lookback is how far back a symbol with no file is fetched from; 0
	// means DefaultLookback. A symbol that already has bars is fetched from
	// its own first bar, so its depth is preserved.
	Lookback time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Result is one symbol's outcome, shaped for the screen that shows it.
type Result struct {
	Symbol string `json:"symbol"`
	// Bars is how many the file holds now, First and Last its range.
	Bars  int    `json:"bars"`
	First string `json:"first,omitempty"`
	Last  string `json:"last,omitempty"`
	// Added is how many bars this refill gained; 0 means already current.
	Added int `json:"added"`
	// Error is why the symbol was left alone, when it was.
	Error string `json:"error,omitempty"`
}

// Run refills every symbol, in the order given, and reports each one. It
// returns a Result per symbol and never an error: one symbol failing is not
// the others' problem, and the caller shows the reasons.
func Run(ctx context.Context, f Fetcher, symbols []string, opts Options) []Result {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	lookback := opts.Lookback
	if lookback <= 0 {
		lookback = DefaultLookback
	}
	out := make([]Result, 0, len(symbols))
	for _, symbol := range symbols {
		out = append(out, one(ctx, f, symbol, opts.Dir, lookback, now()))
	}
	return out
}

func one(ctx context.Context, f Fetcher, symbol, dir string, lookback time.Duration, now time.Time) Result {
	r := Result{Symbol: symbol}
	if !safeSymbol.MatchString(symbol) {
		r.Error = fmt.Sprintf("%q is not a symbol", symbol)
		return r
	}
	path := filepath.Join(dir, symbol+".parquet")

	// An unreadable existing file is not a reason to refuse: a refill is
	// exactly how an operator replaces one. A file that reads is the floor
	// the answer has to clear.
	existing, readErr := bars.Read(path)
	from := day(now.Add(-lookback))
	if readErr == nil && len(existing) > 0 {
		from = day(existing[0].Date)
	}

	series, err := f.Daily(ctx, symbol, from, day(now))
	if err != nil {
		return fill(r, existing, readErr, err.Error())
	}
	if len(series) == 0 {
		return fill(r, existing, readErr, "no bars returned for "+from.Format("2006-01-02")+" onwards")
	}
	if readErr == nil && len(series) < len(existing) {
		return fill(r, existing, readErr, fmt.Sprintf(
			"refused: the answer has %d bars where the file has %d; the file is left alone",
			len(series), len(existing)))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fill(r, existing, readErr, err.Error())
	}
	if err := bars.Write(path, series); err != nil {
		return fill(r, existing, readErr, err.Error())
	}
	r.Bars = len(series)
	r.First = series[0].Date.Format("2006-01-02")
	r.Last = series[len(series)-1].Date.Format("2006-01-02")
	r.Added = len(series) - len(existing)
	if readErr != nil {
		r.Added = len(series)
	}
	return r
}

// fill reports a symbol that was left alone: the error, and what the file
// still holds, so the screen shows the state that is actually on disk.
func fill(r Result, existing []bars.Bar, readErr error, msg string) Result {
	r.Error = msg
	if readErr == nil && len(existing) > 0 {
		r.Bars = len(existing)
		r.First = existing[0].Date.Format("2006-01-02")
		r.Last = existing[len(existing)-1].Date.Format("2006-01-02")
	}
	return r
}

func day(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
