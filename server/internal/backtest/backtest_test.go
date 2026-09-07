package backtest

import (
	"encoding/csv"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/pairs"
)

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// The same three-bar case as research/tests/test_backtest.py, with the same
// hand-worked numbers, so a change to either engine shows up here first.
func TestRunMatchesTheHandWorkedCase(t *testing.T) {
	dates := []time.Time{
		time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC),
	}
	prices := map[string][]float64{"A": {100, 110, 110}, "B": {50, 50, 40}}
	w := []map[string]float64{{"A": 0.5, "B": -0.5}, {"A": 0.5, "B": -0.5}, {"A": 0, "B": 0}}
	res := Run(dates, prices, []string{"A", "B"}, func(t int) map[string]float64 { return w[t] }, Costs{CommissionUSD: 1, SlippageBps: 10})
	want := []float64{9988.0, 10485.5, 11522.86}
	tol := []float64{1e-9, 0.01, 0.05}
	for i, pt := range res.Equity {
		if !near(pt.Equity, want[i], tol[i]) {
			t.Errorf("bar %d equity = %.4f, want %.4f", i, pt.Equity, want[i])
		}
	}
	if len(res.Trades) != 6 {
		t.Errorf("trades = %d, want 6", len(res.Trades))
	}
}

func TestStrategyRefusesMissingSymbols(t *testing.T) {
	st, err := pairs.New([]string{"A", "B"}, strategy.Params{"hedge_ratio": 1, "lookback": 3, "entry_z": 1, "exit_z": 0.5, "max_hold_days": 2}, strategy.Sizing{GrossLeverage: 0.9})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Strategy(st, map[string][]bars.Bar{"A": {{Date: time.Now()}}}, Costs{}); err == nil {
		t.Fatal("want an error for a missing symbol")
	}
}

// The golden equity curve was produced by the Python engine over the same
// bars, weights and costs; the two engines must agree to 1e-6 on every bar.
func TestGoldenEquityParity(t *testing.T) {
	dir := "../../../golden/gld_gdx"
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		t.Skip("no golden directory yet")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "spec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Universe []string           `yaml:"universe"`
		Params   map[string]float64 `yaml:"params"`
		Sizing   struct {
			GrossLeverage float64 `yaml:"gross_leverage"`
		} `yaml:"sizing"`
		Provenance struct {
			CostModel struct {
				CommissionUSD float64 `yaml:"commission_usd"`
				SlippageBps   float64 `yaml:"slippage_bps"`
			} `yaml:"cost_model"`
			TestWindow struct {
				From string `yaml:"from"`
			} `yaml:"test_window"`
		} `yaml:"provenance"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	st, err := pairs.New(spec.Universe, spec.Params, strategy.Sizing{GrossLeverage: spec.Sizing.GrossLeverage})
	if err != nil {
		t.Fatal(err)
	}
	hist := map[string][]bars.Bar{}
	for _, s := range spec.Universe {
		series, err := bars.Read(filepath.Join(dir, "bars_"+s+".parquet"))
		if err != nil {
			t.Fatal(err)
		}
		hist[s] = series
	}
	// The goldens carry a warm-up tail before the test window; the equity
	// curve starts at the window. Replay everything for the state, then run
	// the engine from the first test date, as the Python script did.
	from, err := time.Parse("2006-01-02", spec.Provenance.TestWindow.From)
	if err != nil {
		t.Fatal(err)
	}
	res, err := StrategyFrom(st, hist, from, Costs{CommissionUSD: spec.Provenance.CostModel.CommissionUSD, SlippageBps: spec.Provenance.CostModel.SlippageBps})
	if err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(filepath.Join(dir, "equity_curve.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	rows = rows[1:]
	if len(rows) != len(res.Equity) {
		t.Fatalf("golden has %d bars, engine produced %d", len(rows), len(res.Equity))
	}
	for i, row := range rows {
		want, _ := strconv.ParseFloat(row[1], 64)
		if row[0] != res.Equity[i].Date.Format("2006-01-02") {
			t.Fatalf("bar %d: date %s, golden %s", i, res.Equity[i].Date.Format("2006-01-02"), row[0])
		}
		if !near(res.Equity[i].Equity, want, 1e-6) {
			t.Fatalf("bar %d (%s): equity %.9f, golden %.9f", i, row[0], res.Equity[i].Equity, want)
		}
	}
}
