package backtest

import (
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
)

func days(n int) []time.Time {
	out := make([]time.Time, n)
	for i := range out {
		out[i] = time.Date(2024, 1, 1+i, 0, 0, 0, 0, time.UTC)
	}
	return out
}

func weightsOf(w []map[string]float64) Weights { return func(t int) map[string]float64 { return w[t] } }

// The same cases as research/tests/test_execution.py, with the same
// hand-worked numbers.
func TestNextOpenFillsThePreviousTargetAtTheOpen(t *testing.T) {
	closes := map[string][]float64{"A": {100, 110, 120}}
	opens := map[string][]float64{"A": {99, 105, 118}}
	w := []map[string]float64{{"A": 1}, {"A": 1}, {"A": 1}}
	res := RunNextOpen(days(3), closes, opens, []string{"A"}, weightsOf(w), Costs{})
	want := []float64{10_000, 10_000 / 105.0 * 110, 10_000 / 105.0 * 120}
	for i, pt := range res.Equity {
		if !near(pt.Equity, want[i], 1e-9) {
			t.Errorf("bar %d: equity %.6f, want %.6f", i, pt.Equity, want[i])
		}
	}
	if len(res.Trades) != 1 || res.Trades[0].Price != 105 || !res.Trades[0].Date.Equal(days(3)[1]) {
		t.Errorf("trades = %+v, want one at 105 on the second bar", res.Trades)
	}
}

func TestNextOpenFirstBarAndFinalSignalDoNotTrade(t *testing.T) {
	flat := map[string][]float64{"A": {100, 100, 100}}
	w := []map[string]float64{{"A": 0}, {"A": 0}, {"A": 1}}
	res := RunNextOpen(days(3), flat, flat, []string{"A"}, weightsOf(w), Costs{})
	if len(res.Trades) != 0 {
		t.Errorf("trades = %+v, want none", res.Trades)
	}
	for i, pt := range res.Equity {
		if pt.Equity != 10_000 {
			t.Errorf("bar %d: equity %v, want 10000", i, pt.Equity)
		}
	}
}

func TestNextOpenChargesCostsAtTheFillPrice(t *testing.T) {
	closes := map[string][]float64{"A": {100, 100}}
	opens := map[string][]float64{"A": {100, 200}}
	w := []map[string]float64{{"A": 0.5}, {"A": 0.5}}
	res := RunNextOpen(days(2), closes, opens, []string{"A"}, weightsOf(w), Costs{CommissionUSD: 1, SlippageBps: 10})
	// 25 shares at 200 = 5,000 traded; slippage 5, commission 1; marked at 100.
	if !near(res.Equity[1].Equity, 7_494, 1e-9) {
		t.Errorf("equity = %.6f, want 7494", res.Equity[1].Equity)
	}
}

func TestNextOpenSizesFromEquityAtTheOpen(t *testing.T) {
	closes := map[string][]float64{"A": {100, 100, 100}}
	opens := map[string][]float64{"A": {100, 100, 80}}
	w := []map[string]float64{{"A": 1}, {"A": 0.5}, {"A": 0.5}}
	res := RunNextOpen(days(3), closes, opens, []string{"A"}, weightsOf(w), Costs{})
	if !near(res.Equity[2].Equity, 9_000, 1e-9) {
		t.Errorf("equity = %.6f, want 9000", res.Equity[2].Equity)
	}
	if len(res.Trades) != 2 || res.Trades[0].Qty != 100 || res.Trades[1].Qty != -50 {
		t.Errorf("trades = %+v", res.Trades)
	}
}

func TestParseFill(t *testing.T) {
	for _, s := range []string{"next_open", "same_close_legacy"} {
		if f, err := ParseFill(s); err != nil || string(f) != s {
			t.Errorf("ParseFill(%q) = %q, %v", s, f, err)
		}
	}
	if _, err := ParseFill("at_noon"); err == nil {
		t.Error("want an error for an unknown fill")
	}
}

// The synthetic next-open fixture (research/scripts/golden_next_open.py):
// gaps, a split-like factor change, target flips, a no-op target and a
// final unfilled signal. The Python engine wrote the curve; this engine must
// reproduce it to 1e-6.
func TestNextOpenGoldenParity(t *testing.T) {
	dir := "../../../golden/next_open"
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		t.Skip("no golden directory yet")
	}
	symbols := []string{"A", "B"}
	hist := map[string][]bars.Bar{}
	for _, s := range symbols {
		series, err := bars.Read(filepath.Join(dir, "bars_"+s+".parquet"))
		if err != nil {
			t.Fatal(err)
		}
		hist[s] = series
	}
	dates, aligned := bars.Align(hist)
	closes, opens := map[string][]float64{}, map[string][]float64{}
	for s, series := range aligned {
		for _, b := range series {
			closes[s] = append(closes[s], b.AdjClose)
			opens[s] = append(opens[s], bars.AdjustedOpen(b))
		}
	}
	rows := readCSV(t, filepath.Join(dir, "weights.csv"))
	byDate := map[string]map[string]float64{}
	for _, row := range rows {
		if byDate[row[0]] == nil {
			byDate[row[0]] = map[string]float64{}
		}
		byDate[row[0]][row[1]], _ = strconv.ParseFloat(row[2], 64)
	}
	weights := func(t int) map[string]float64 { return byDate[dates[t].Format("2006-01-02")] }
	res := RunNextOpen(dates, closes, opens, symbols, weights, Costs{CommissionUSD: 1, SlippageBps: 10})

	curve := readCSV(t, filepath.Join(dir, "equity_curve.csv"))
	if len(curve) != len(res.Equity) {
		t.Fatalf("golden has %d bars, engine produced %d", len(curve), len(res.Equity))
	}
	for i, row := range curve {
		want, _ := strconv.ParseFloat(row[1], 64)
		if row[0] != res.Equity[i].Date.Format("2006-01-02") {
			t.Fatalf("bar %d: date %s, golden %s", i, res.Equity[i].Date.Format("2006-01-02"), row[0])
		}
		if !near(res.Equity[i].Equity, want, 1e-6) {
			t.Fatalf("bar %d (%s): equity %.9f, golden %.9f", i, row[0], res.Equity[i].Equity, want)
		}
	}
}

// readCSV returns the rows after the header.
func readCSV(t *testing.T, path string) [][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows[1:]
}
