// Package bars is the daily bar: the one record every other package reads.
//
// Bars come from Parquet files the research side writes, one file per symbol
// (docs/PLAN.md §4.1). This package owns the Bar type, the reader, and the
// invariants every loaded series must satisfy — strictly increasing dates,
// positive prices, low ≤ open/close ≤ high, adjclose ≤ close — which are
// checked on load, in both languages, and fail closed.
package bars

import "time"

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
