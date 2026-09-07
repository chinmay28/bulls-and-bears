package metrics

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestDailyReturns(t *testing.T) {
	got := DailyReturns([]float64{100, 110, 99})
	// 110/100-1 = 0.1; 99/110-1 = -0.1
	if len(got) != 2 || !near(got[0], 0.1, 1e-12) || !near(got[1], -0.1, 1e-12) {
		t.Errorf("returns = %v", got)
	}
	if DailyReturns([]float64{100}) != nil {
		t.Error("one point has no returns")
	}
}

func TestSharpeByHand(t *testing.T) {
	// Returns 0.01, -0.01, 0.02, 0.00: mean 0.005;
	// deviations 0.005, -0.015, 0.015, -0.005; squares sum 0.0005;
	// sample variance 0.0005/3; sd = sqrt(0.000166667) = 0.0129099;
	// sharpe = 0.005/0.0129099*sqrt(252) = 0.387298*15.8745 = 6.14817.
	got, err := Sharpe([]float64{0.01, -0.01, 0.02, 0.00})
	if err != nil {
		t.Fatal(err)
	}
	if !near(got, 6.148170, 1e-5) {
		t.Errorf("sharpe = %v, want 6.14817", got)
	}
}

func TestSharpeUndefined(t *testing.T) {
	for _, r := range [][]float64{nil, {0.1}, {0.01, 0.01, 0.01}} {
		if _, err := Sharpe(r); !errors.Is(err, ErrUndefined) {
			t.Errorf("Sharpe(%v) err = %v, want ErrUndefined", r, err)
		}
	}
}

func TestDrawdown(t *testing.T) {
	cases := []struct {
		name     string
		equity   []float64
		dd       float64
		duration int
	}{
		{"empty", nil, 0, 0},
		{"flat", []float64{1, 1, 1}, 0, 0},
		{"monotonic rise", []float64{1, 2, 3}, 0, 0},
		// peak 100, trough 80: -0.2; under water on 90, 80, 95 = 3 days, recovers at 101.
		{"single dip", []float64{100, 90, 80, 95, 101}, -0.2, 3},
		// two dips: first 2 days (-0.1), second 3 days (-0.15) and never recovers.
		{"dip at the end never recovered", []float64{100, 95, 90, 110, 100, 95, 93.5}, -0.15, 3},
		// equal to the peak is not under water.
		{"touching the peak", []float64{100, 90, 100, 90}, -0.1, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MaxDrawdown(c.equity); !near(got, c.dd, 1e-12) {
				t.Errorf("MaxDrawdown = %v, want %v", got, c.dd)
			}
			if got := MaxDrawdownDuration(c.equity); got != c.duration {
				t.Errorf("MaxDrawdownDuration = %d, want %d", got, c.duration)
			}
		})
	}
}

func TestSummarize(t *testing.T) {
	s, err := Summarize([]float64{100, 101, 100, 102})
	if err != nil {
		t.Fatal(err)
	}
	if s.Days != 4 || !near(s.TotalReturn, 0.02, 1e-12) || !near(s.MaxDrawdown, -1.0/101, 1e-12) || s.MaxDrawdownDuration != 1 {
		t.Errorf("summary = %+v", s)
	}
	if _, err := Summarize([]float64{100, 100, 100}); !errors.Is(err, ErrUndefined) {
		t.Errorf("flat curve err = %v", err)
	}
}

// The golden equity curve and metrics are written by the research side
// (docs/PLAN.md §4.4); this holds the two implementations to 1e-6.
func TestGoldenParity(t *testing.T) {
	dir := "../../../golden/gld_gdx"
	f, err := os.Open(filepath.Join(dir, "equity_curve.csv"))
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("no golden equity curve yet")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var equity []float64
	for i, row := range rows {
		if i == 0 {
			continue // header
		}
		v, err := strconv.ParseFloat(row[1], 64)
		if err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
		equity = append(equity, v)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "metrics.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		Sharpe      float64 `json:"sharpe"`
		MaxDrawdown float64 `json:"max_drawdown"`
		Duration    int     `json:"max_drawdown_duration"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	got, err := Summarize(equity)
	if err != nil {
		t.Fatal(err)
	}
	if !near(got.Sharpe, want.Sharpe, 1e-6) {
		t.Errorf("sharpe = %.9f, python %.9f", got.Sharpe, want.Sharpe)
	}
	if !near(got.MaxDrawdown, want.MaxDrawdown, 1e-6) {
		t.Errorf("max drawdown = %.9f, python %.9f", got.MaxDrawdown, want.MaxDrawdown)
	}
	if got.MaxDrawdownDuration != want.Duration {
		t.Errorf("duration = %d, python %d", got.MaxDrawdownDuration, want.Duration)
	}
}
