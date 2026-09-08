// Package paritytest is the one golden-file check every strategy package
// runs: load the research side's bars, spec and expected signals from
// golden/<name>, build the strategy from the spec, replay it, and hold each
// weight to the contract's tolerance (docs/PLAN.md §4.3). It is a test
// helper, imported only from _test files, so that a new strategy's parity
// test is the three lines that name its golden directory and constructor
// rather than a copy of this file.
package paritytest

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
)

// Tolerance is the contract's allowance for float order-of-operations
// differences between pandas and Go.
const Tolerance = 1e-9

// Weighter is what a strategy must offer to be checked: its targets for
// every aligned bar, in bars.Align order.
type Weighter interface {
	Weights(hist map[string][]bars.Bar) ([]map[string]float64, error)
}

// Build makes the strategy from the golden spec's universe, params and sizing.
type Build func(universe []string, params strategy.Params, sizing strategy.Sizing) (Weighter, error)

// spec is the slice of the strategy spec the check needs.
type spec struct {
	Strategy string             `yaml:"strategy"`
	Universe []string           `yaml:"universe"`
	Params   map[string]float64 `yaml:"params"`
	Sizing   struct {
		GrossLeverage        float64 `yaml:"gross_leverage"`
		MaxNotionalPerLegUSD float64 `yaml:"max_notional_per_leg_usd"`
	} `yaml:"sizing"`
}

// Run checks every row of goldenDir/expected_signals.csv against the
// strategy build makes. It skips, rather than fails, until the research side
// has produced the golden directory; once it exists every row must match.
func Run(t *testing.T, goldenDir, name string, build Build) {
	t.Helper()
	if _, err := os.Stat(goldenDir); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no golden directory at %s yet", goldenDir)
	} else if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(goldenDir, "spec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var sp spec
	if err := yaml.Unmarshal(raw, &sp); err != nil {
		t.Fatalf("spec.yaml: %v", err)
	}
	if sp.Strategy != "" && sp.Strategy != name {
		t.Fatalf("spec.yaml is for strategy %q, this test checks %q", sp.Strategy, name)
	}
	st, err := build(sp.Universe, strategy.Params(sp.Params), strategy.Sizing{GrossLeverage: sp.Sizing.GrossLeverage, MaxNotionalPerLegUSD: sp.Sizing.MaxNotionalPerLegUSD})
	if err != nil {
		t.Fatalf("spec.yaml: %v", err)
	}
	hist := map[string][]bars.Bar{}
	for _, sym := range sp.Universe {
		path := filepath.Join(goldenDir, "bars_"+sym+".parquet")
		series, err := bars.Read(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		hist[sym] = series
	}
	dates, aligned := bars.Align(hist)
	weights, err := st.Weights(aligned)
	if err != nil {
		t.Fatal(err)
	}
	if len(weights) != len(dates) {
		t.Fatalf("strategy gave %d bars of weights for %d aligned dates", len(weights), len(dates))
	}
	byDate := make(map[string]map[string]float64, len(dates))
	for i, d := range dates {
		byDate[d.UTC().Format("2006-01-02")] = weights[i]
	}

	rows, err := readExpected(filepath.Join(goldenDir, "expected_signals.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if want := len(sp.Universe) * len(dates); len(rows) != want {
		t.Errorf("expected_signals.csv has %d rows, Go aligned %d bars x %d symbols = %d", len(rows), len(dates), len(sp.Universe), want)
	}
	var (
		mismatches int
		first      string
	)
	for _, row := range rows {
		w, ok := byDate[row.date]
		if !ok {
			mismatches++
			if first == "" {
				first = fmt.Sprintf("%s %s: date in golden but not in Go alignment", row.date, row.symbol)
			}
			continue
		}
		got, ok := w[row.symbol]
		if !ok {
			mismatches++
			if first == "" {
				first = fmt.Sprintf("%s %s: symbol not in Go weights %v", row.date, row.symbol, w)
			}
			continue
		}
		if diff := math.Abs(got - row.weight); !(diff <= Tolerance) {
			mismatches++
			if first == "" {
				first = fmt.Sprintf("%s %s: Go %.17g, Python %.17g, off by %.3g (Go weights %v)", row.date, row.symbol, got, row.weight, diff, w)
			}
		}
	}
	if mismatches > 0 {
		t.Fatalf("%d of %d rows differ beyond %g; first: %s", mismatches, len(rows), Tolerance, first)
	}
}

type expectedRow struct {
	date, symbol string
	weight       float64
}

// readExpected parses the golden CSV by header name, so a column reorder on
// the Python side is not a silent mismatch.
func readExpected(path string) ([]expectedRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return nil, err
	}
	col := map[string]int{}
	for i, h := range header {
		col[h] = i
	}
	for _, need := range []string{"date", "symbol", "target_weight"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("%s: no %q column in %v", path, need, header)
		}
	}
	var rows []expectedRow
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		w, err := strconv.ParseFloat(rec[col["target_weight"]], 64)
		if err != nil {
			return nil, fmt.Errorf("%s: row %v: %w", path, rec, err)
		}
		rows = append(rows, expectedRow{date: rec[col["date"]], symbol: rec[col["symbol"]], weight: w})
	}
	return rows, nil
}
