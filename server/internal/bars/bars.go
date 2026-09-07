// Package bars is the daily bar: the one record every other package reads.
//
// Bars come from Parquet files the research side writes, one file per symbol
// (docs/PLAN.md §4.1). This package owns the Bar type, the reader, and the
// invariants every loaded series must satisfy — strictly increasing dates,
// positive prices, low ≤ open/close ≤ high, adjclose ≤ close — which are
// checked on load, in both languages, and fail closed.
package bars

import (
	"errors"
	"time"
)

// Bar is one trading day of one symbol.
type Bar struct {
	// Date is the trading day, exchange-local, at midnight UTC.
	Date            time.Time
	Open, High, Low float64
	Close, AdjClose float64
	Volume          int64
	// Source names where the bar came from: "yahoo", "robinhood", other.
	Source string
	// FetchedAt is when it was fetched, UTC.
	FetchedAt time.Time
}

// Read loads one symbol's bars from a Parquet file written to the schema in
// docs/PLAN.md §4.1, oldest first, and checks the invariants before handing
// them back. A file that fails a check is an error, never a partial series.
func Read(path string) ([]Bar, error) {
	return nil, errNotImplemented
}

// Check enforces the invariants on a series: strictly increasing dates, every
// price positive, low ≤ min(open, close) ≤ max(open, close) ≤ high, and
// adjclose ≤ close. The first violation is the error, with its index.
func Check(series []Bar) error {
	return errNotImplemented
}

var errNotImplemented = errors.New("bars: not implemented yet")
