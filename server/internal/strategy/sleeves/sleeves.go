// Package sleeves is what the one-sleeve-per-risk-asset strategies share:
// the universe shape [R_1 … R_k, H] with the haven last, the params
// checks every constructor makes, and the inner join of every symbol's
// adjclose on date (docs/CONTRACTS.md, Bars). It decides nothing about
// trading; each strategy's answer still depends only on its own file and
// the contract, and this package is the part of the contract they hold in
// common.
package sleeves

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
)

// Universe splits [R_1 … R_k, H] into the risk assets and the haven,
// refusing an empty symbol, a repeat, or a universe with no risk asset.
func Universe(pkg string, universe []string) (risk []string, haven string, err error) {
	if len(universe) < 2 {
		return nil, "", fmt.Errorf("%s: universe needs at least one risk asset and the haven, got %v", pkg, universe)
	}
	seen := map[string]bool{}
	for _, s := range universe {
		if s == "" {
			return nil, "", fmt.Errorf("%s: universe has an empty symbol: %v", pkg, universe)
		}
		if seen[s] {
			return nil, "", fmt.Errorf("%s: universe names %q twice", pkg, s)
		}
		seen[s] = true
	}
	return append([]string(nil), universe[:len(universe)-1]...), universe[len(universe)-1], nil
}

// Known refuses a params block with a key outside names or missing one.
func Known(pkg string, params strategy.Params, names ...string) error {
	known := map[string]bool{}
	for _, n := range names {
		known[n] = true
	}
	var unknown, missing []string
	for name := range params {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	for _, name := range names {
		if _, ok := params[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(unknown)
	sort.Strings(missing)
	if len(unknown) > 0 {
		return fmt.Errorf("%s: unknown params %s", pkg, strings.Join(unknown, ", "))
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s: missing params %s", pkg, strings.Join(missing, ", "))
	}
	return nil
}

// WholeNumber checks that v is an integer of at least min.
func WholeNumber(pkg, name string, v float64, min int) (int, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) {
		return 0, fmt.Errorf("%s: %s must be a whole number, got %v", pkg, name, v)
	}
	if v < float64(min) {
		return 0, fmt.Errorf("%s: %s must be >= %d, got %v", pkg, name, min, v)
	}
	return int(v), nil
}

// Align inner-joins every symbol's series on calendar date, in the haven's
// order, and returns the shared dates with each symbol's adjclose. A date
// any symbol lacks is dropped before anything is computed.
func Align(pkg string, universe []string, haven string, hist map[string][]bars.Bar) ([]time.Time, map[string][]float64, error) {
	dates, aligned, err := AlignBars(pkg, universe, haven, hist)
	if err != nil {
		return nil, nil, err
	}
	px := make(map[string][]float64, len(universe))
	for sym, series := range aligned {
		p := make([]float64, len(series))
		for i, b := range series {
			p[i] = b.AdjClose
		}
		px[sym] = p
	}
	return dates, px, nil
}

// AlignBars is Align keeping the whole bar, for a strategy that reads the
// adjusted high or low as well as the close.
func AlignBars(pkg string, universe []string, haven string, hist map[string][]bars.Bar) ([]time.Time, map[string][]bars.Bar, error) {
	byDay := make(map[string]map[day]bars.Bar, len(universe))
	for _, sym := range universe {
		series, ok := hist[sym]
		if !ok {
			return nil, nil, fmt.Errorf("%s: no bars for %s", pkg, sym)
		}
		idx := make(map[day]bars.Bar, len(series))
		for _, b := range series {
			idx[dayOf(b.Date)] = b
		}
		byDay[sym] = idx
	}
	var (
		dates []time.Time
		prev  day
	)
	out := make(map[string][]bars.Bar, len(universe))
	for _, bar := range hist[haven] {
		d := dayOf(bar.Date)
		shared := true
		for _, sym := range universe {
			if _, ok := byDay[sym][d]; !ok {
				shared = false
				break
			}
		}
		if !shared {
			continue
		}
		if len(dates) > 0 && !prev.before(d) {
			return nil, nil, fmt.Errorf("%s: %s bars are not strictly increasing by date at %s", pkg, haven, bar.Date.Format("2006-01-02"))
		}
		prev = d
		dates = append(dates, bar.Date)
		for _, sym := range universe {
			out[sym] = append(out[sym], byDay[sym][d])
		}
	}
	return dates, out, nil
}

type day struct {
	y int
	m time.Month
	d int
}

func dayOf(t time.Time) day {
	y, m, d := t.UTC().Date()
	return day{y, m, d}
}

func (a day) before(b day) bool {
	if a.y != b.y {
		return a.y < b.y
	}
	if a.m != b.m {
		return a.m < b.m
	}
	return a.d < b.d
}
