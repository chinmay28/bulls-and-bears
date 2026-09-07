// Package bars is the daily bar: the one record every other package reads.
//
// Bars come from Parquet files the research side writes, one file per symbol
// (docs/PLAN.md §4.1). This package owns the Bar type, the reader and writer
// for that file, the invariants every loaded series must satisfy — strictly
// increasing dates, positive prices, low ≤ open/close ≤ high, adjclose ≤
// close — which are checked on load, in both languages, and fail closed, and
// the small pure helpers the plan asks for on top of a series: aligning
// symbols on date, windowing, staleness, mixed sources.
//
// It does not fetch anything, know about the network, or decide what to do
// about a stale or mixed series: it reports, and the caller halts or warns.
package bars

import (
	"fmt"
	"math"
	"os"
	"sort"
	"time"

	"github.com/parquet-go/parquet-go"
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

// row is the on-disk shape of a Bar: the §4.1 column names and logical types,
// kept off the public type so the storage encoding is not part of the API.
// The tags pick the same physical types pyarrow writes — int32 DATE, int64
// TIMESTAMP(MICROS, UTC), BYTE_ARRAY UTF8 — so a file written here reads back
// in pandas as date32/timestamp[us], and one written by pandas reads here.
// parquet-go matches columns by name, so the file's column order does not
// matter and columns it does not know (pandas' __index_level_0__) are dropped.
type row struct {
	// Date is days since the Unix epoch, which is what a DATE column holds.
	// Converted by hand: it is one line each way and sidesteps how a given
	// parquet-go release maps time.Time onto DATE.
	Date      int32     `parquet:"date,date"`
	Open      float64   `parquet:"open"`
	High      float64   `parquet:"high"`
	Low       float64   `parquet:"low"`
	Close     float64   `parquet:"close"`
	AdjClose  float64   `parquet:"adjclose"`
	Volume    int64     `parquet:"volume"`
	Source    string    `parquet:"source,dict"`
	FetchedAt time.Time `parquet:"fetched_at,timestamp(microsecond:utc)"`
}

// adjCloseTolerance is how far above close adjclose may sit and still count
// as equal: an adjustment factor of exactly 1 goes through float arithmetic
// in the fetcher and can land a few ulps high.
const adjCloseTolerance = 1e-9

// Read loads one symbol's bars from a Parquet file written to the schema in
// docs/PLAN.md §4.1, oldest first, and checks the invariants before handing
// them back. A file that fails a check is an error, never a partial series.
// A file with no rows is an empty series, not an error: whether nothing is
// acceptable is the caller's call.
func Read(path string) (series []Bar, err error) {
	// parquet-go reports most problems as errors, but a file whose columns
	// cannot be mapped onto the schema panics inside the reader. A bad file
	// must halt the caller, not the process.
	defer func() {
		if r := recover(); r != nil {
			series, err = nil, fmt.Errorf("bars: read %s: %v", path, r)
		}
	}()

	rows, err := parquet.ReadFile[row](path)
	if err != nil {
		return nil, fmt.Errorf("bars: read %s: %w", path, err)
	}
	series = make([]Bar, len(rows))
	for i, r := range rows {
		series[i] = Bar{
			Date:      time.Unix(int64(r.Date)*86400, 0).UTC(),
			Open:      r.Open,
			High:      r.High,
			Low:       r.Low,
			Close:     r.Close,
			AdjClose:  r.AdjClose,
			Volume:    r.Volume,
			Source:    r.Source,
			FetchedAt: r.FetchedAt.UTC(),
		}
	}
	if err := Check(series); err != nil {
		return nil, fmt.Errorf("bars: read %s: %w", path, err)
	}
	return series, nil
}

// Write stores a series to path in the §4.1 schema, oldest first, so Go can
// refresh a tail the research side will read back. It refuses a series that
// fails Check: a file that would not load is not worth writing. The file is
// written whole and renamed into place so a reader never sees a torn one.
func Write(path string, series []Bar) error {
	if err := Check(series); err != nil {
		return fmt.Errorf("bars: write %s: %w", path, err)
	}
	rows := make([]row, len(series))
	for i, b := range series {
		rows[i] = row{
			Date:      int32(day(b.Date).Unix() / 86400),
			Open:      b.Open,
			High:      b.High,
			Low:       b.Low,
			Close:     b.Close,
			AdjClose:  b.AdjClose,
			Volume:    b.Volume,
			Source:    b.Source,
			FetchedAt: b.FetchedAt.UTC(),
		}
	}
	tmp := path + ".tmp"
	if err := parquet.WriteFile(tmp, rows); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("bars: write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("bars: write %s: %w", path, err)
	}
	return nil
}

// Check enforces the invariants on a series: strictly increasing dates, every
// price positive, low ≤ min(open, close) ≤ max(open, close) ≤ high, and
// adjclose ≤ close (within adjCloseTolerance, relative). The first violation
// is the error, naming the bar's index, date and the invariant it broke. An
// empty series passes.
func Check(series []Bar) error {
	for i, b := range series {
		fail := func(format string, args ...any) error {
			return fmt.Errorf("bar %d (%s): %s", i, b.Date.UTC().Format("2006-01-02"), fmt.Sprintf(format, args...))
		}
		if b.Date.IsZero() {
			return fail("date is unset")
		}
		if i > 0 && !b.Date.After(series[i-1].Date) {
			return fail("date is not after the previous bar's %s", series[i-1].Date.UTC().Format("2006-01-02"))
		}
		for _, p := range []struct {
			name string
			v    float64
		}{{"open", b.Open}, {"high", b.High}, {"low", b.Low}, {"close", b.Close}, {"adjclose", b.AdjClose}} {
			// NaN fails the comparison too, which is what we want: a price
			// that is not a number is not positive.
			if !(p.v > 0) || math.IsInf(p.v, 0) {
				return fail("%s %v is not positive", p.name, p.v)
			}
		}
		if lo := math.Min(b.Open, b.Close); b.Low > lo {
			return fail("low %v is above min(open, close) %v", b.Low, lo)
		}
		if hi := math.Max(b.Open, b.Close); hi > b.High {
			return fail("high %v is below max(open, close) %v", b.High, hi)
		}
		if b.AdjClose > b.Close*(1+adjCloseTolerance) {
			return fail("adjclose %v is above close %v", b.AdjClose, b.Close)
		}
	}
	return nil
}

// Align inner-joins the series on Date (docs/CONTRACTS.md, Bars): a date any
// symbol lacks is dropped from all of them. It returns the common dates,
// oldest first, and each symbol's bars on exactly those dates in that order,
// so index i means the same day in every returned slice. Each input series is
// expected to have passed Check; with duplicate dates the last one wins.
func Align(series map[string][]Bar) ([]time.Time, map[string][]Bar) {
	if len(series) == 0 {
		return nil, map[string][]Bar{}
	}
	byDate := make(map[string]map[int64]Bar, len(series))
	count := map[int64]int{}
	for sym, bars := range series {
		idx := make(map[int64]Bar, len(bars))
		for _, b := range bars {
			k := day(b.Date).Unix()
			if _, seen := idx[k]; !seen {
				count[k]++
			}
			idx[k] = b
		}
		byDate[sym] = idx
	}

	var keys []int64
	for k, n := range count {
		if n == len(series) {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	dates := make([]time.Time, len(keys))
	for i, k := range keys {
		dates[i] = time.Unix(k, 0).UTC()
	}
	aligned := make(map[string][]Bar, len(series))
	for sym, idx := range byDate {
		out := make([]Bar, len(keys))
		for i, k := range keys {
			out[i] = idx[k]
		}
		aligned[sym] = out
	}
	return dates, aligned
}

// Window returns the bars with from ≤ Date ≤ to, both ends inclusive, in the
// order they appear. The result is a fresh slice; the input is untouched.
func Window(series []Bar, from, to time.Time) []Bar {
	var out []Bar
	for _, b := range series {
		if !b.Date.Before(from) && !b.Date.After(to) {
			out = append(out, b)
		}
	}
	return out
}

// Stale reports whether the last bar is older than maxTradingDays trading
// days: it counts the weekdays after the last bar's date up to and including
// now's date, both in UTC, and is true when that exceeds maxTradingDays.
// Holidays are not known here, so a long weekend counts as trading days
// missed; the plan only asks for a warning at 3, and the false alarm after a
// holiday is cheaper than a real gap going unnoticed. An empty series is
// stale — there is nothing there to be fresh.
func Stale(series []Bar, now time.Time, maxTradingDays int) bool {
	if len(series) == 0 {
		return true
	}
	last := day(series[len(series)-1].Date)
	today := day(now)
	missed := 0
	for d := last.AddDate(0, 0, 1); !d.After(today); d = d.AddDate(0, 0, 1) {
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday {
			missed++
			if missed > maxTradingDays {
				return true
			}
		}
	}
	return false
}

// MixedSources reports whether the series draws on more than one Source. The
// plan makes that an error inside a backtest window unless
// --allow-mixed-sources is set; this only detects it.
func MixedSources(series []Bar) bool {
	if len(series) == 0 {
		return false
	}
	first := series[0].Source
	for _, b := range series[1:] {
		if b.Source != first {
			return true
		}
	}
	return false
}

// day is t's calendar day in UTC at midnight: the form every Date takes, and
// the key two bars on the same day share whatever clock they were built from.
func day(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
