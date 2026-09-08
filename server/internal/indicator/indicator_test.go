package indicator

import (
	"encoding/csv"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// defined checks the undefined prefix and returns the defined values.
func defined(t *testing.T, name string, s Series, prefix int) []float64 {
	t.Helper()
	for i := range s.Defined {
		if s.Defined[i] != (i >= prefix) {
			t.Fatalf("%s: bar %d defined = %v, want %v", name, i, s.Defined[i], i >= prefix)
		}
	}
	return s.Value[prefix:]
}

func wantNear(t *testing.T, name string, got, want []float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d values, want %d", name, len(got), len(want))
	}
	for i := range got {
		if !near(got[i], want[i], 1e-12) {
			t.Errorf("%s[%d] = %.17g, want %.17g", name, i, got[i], want[i])
		}
	}
}

// The same hand-computed cases as research/tests/test_indicators.py.
func TestRollingMeanSDZScore(t *testing.T) {
	x := []float64{1, 2, 4, 7, 7}
	wantNear(t, "mean", defined(t, "mean", RollingMean(x, 3), 2), []float64{7.0 / 3, 13.0 / 3, 6})
	wantNear(t, "sd", defined(t, "sd", RollingSampleSD(x, 2), 1), []float64{math.Sqrt(0.5), math.Sqrt(2), math.Sqrt(4.5), 0})
	z := RollingZScore(x, 2)
	if z.Defined[0] || z.Defined[4] || !z.Defined[1] {
		t.Errorf("zscore defined = %v", z.Defined)
	}
	if !near(z.Value[1], (2-1.5)/math.Sqrt(0.5), 1e-12) {
		t.Errorf("z[1] = %v", z.Value[1])
	}
	if s := RollingMean(x, 1); s.Defined[4] {
		t.Error("a lookback of 1 must be undefined everywhere")
	}
}

func TestRollingReturn(t *testing.T) {
	p := []float64{100, 110, 99, 108.9}
	wantNear(t, "r2", defined(t, "r2", RollingReturn(p, 2), 2), []float64{-0.01, -0.01})
	wantNear(t, "r1", defined(t, "r1", RollingReturn(p, 1), 1), []float64{0.1, -0.1, 0.1})
	if s := RollingReturn(p, 4); s.Defined[3] {
		t.Error("defined on a too-short series")
	}
}

func TestSMA(t *testing.T) {
	s := SMA([]float64{10, 10, 10, 10}, 3)
	if s.Defined[0] || s.Defined[1] || !s.Defined[2] || s.Value[2] != 10 || s.Value[3] != 10 {
		t.Errorf("sma = %+v", s)
	}
	wantNear(t, "sma2", defined(t, "sma2", SMA([]float64{1, 2, 3, 4}, 2), 1), []float64{1.5, 2.5, 3.5})
	if SMA([]float64{1}, 2).Defined[0] {
		t.Error("defined on a short series")
	}
}

func TestDonchianUsesPriorBarsOnly(t *testing.T) {
	up := DonchianUpper([]float64{5, 7, 6, 9, 4}, 2)
	wantNear(t, "upper", defined(t, "upper", up, 2), []float64{7, 7, 9})
	lo := DonchianLower([]float64{5, 3, 6, 2, 4}, 3)
	wantNear(t, "lower", defined(t, "lower", lo, 3), []float64{3, 2})
}

func TestRealisedVol(t *testing.T) {
	p := []float64{100, 110, 99, 108.9, 108.9, 120}
	r := make([]float64, 5)
	for i := range r {
		r[i] = p[i+1]/p[i] - 1
	}
	v := defined(t, "vol", RealisedVol(p, 2), 2)
	if !near(v[0], sampleSD(r[0:2]), 1e-15) || !near(v[3], sampleSD(r[3:5]), 1e-15) {
		t.Errorf("vol = %v", v)
	}
	flat := defined(t, "flat", RealisedVol([]float64{1, 1, 1, 1}, 2), 2)
	if flat[0] != 0 || flat[1] != 0 {
		t.Errorf("flat vol = %v, want zeros (defined)", flat)
	}
	if RealisedVol([]float64{1}, 2).Defined[0] {
		t.Error("defined on one bar")
	}
}

func TestTrueRangeAndATR(t *testing.T) {
	h := []float64{10, 12, 11, 15}
	lo := []float64{9, 10, 9, 12}
	c := []float64{9.5, 11, 10, 14}
	wantNear(t, "tr", defined(t, "tr", TrueRange(h, lo, c), 1), []float64{2.5, 2, 5})
	wantNear(t, "atr", defined(t, "atr", ATR(h, lo, c, 2), 2), []float64{2.25, (2.25 + 5) / 2})
}

func TestWilderRSI(t *testing.T) {
	out := defined(t, "rsi", WilderRSI([]float64{10, 11, 10, 12, 12, 9}, 2), 2)
	want := []float64{50, 100 - 100/(1+1.25/0.25), 100 - 100/(1+0.625/0.125), 100 - 100/(1+0.3125/1.5625)}
	wantNear(t, "rsi", out, want)
	wantNear(t, "only gains", defined(t, "g", WilderRSI([]float64{5, 6, 7, 8}, 2), 2), []float64{100, 100})
	wantNear(t, "flat", defined(t, "f", WilderRSI([]float64{5, 5, 5, 5}, 2), 2), []float64{50, 50})
	wantNear(t, "only losses", defined(t, "l", WilderRSI([]float64{5, 4, 3}, 2), 2), []float64{0})
	if WilderRSI([]float64{5, 6}, 2).Defined[1] {
		t.Error("defined on a short series")
	}
}

// TestGoldenParity reproduces every column of golden/indicators/expected.csv
// from series.csv to 1e-9; NaN there means undefined here.
func TestGoldenParity(t *testing.T) {
	dir := "../../../golden/indicators"
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		t.Skip("no golden directory yet")
	}
	series := readCSV(t, filepath.Join(dir, "series.csv"))
	col := func(rows [][]string, header []string, name string) []float64 {
		idx := -1
		for i, h := range header {
			if h == name {
				idx = i
			}
		}
		if idx < 0 {
			t.Fatalf("no column %s", name)
		}
		out := make([]float64, len(rows))
		for i, r := range rows {
			v, err := strconv.ParseFloat(r[idx], 64)
			if err != nil {
				t.Fatalf("%s row %d: %v", name, i, err)
			}
			out[i] = v
		}
		return out
	}
	sh, srows := series[0], series[1:]
	c, h, lo := col(srows, sh, "close"), col(srows, sh, "high"), col(srows, sh, "low")
	expected := readCSV(t, filepath.Join(dir, "expected.csv"))
	eh, erows := expected[0], expected[1:]
	got := map[string]Series{
		"rolling_mean_20":      RollingMean(c, 20),
		"rolling_sample_sd_20": RollingSampleSD(c, 20),
		"rolling_zscore_20":    RollingZScore(c, 20),
		"rolling_return_21":    RollingReturn(c, 21),
		"sma_50":               SMA(c, 50),
		"donchian_upper_20":    DonchianUpper(h, 20),
		"donchian_lower_10":    DonchianLower(lo, 10),
		"realised_vol_21":      RealisedVol(c, 21),
		"true_range":           TrueRange(h, lo, c),
		"atr_14":               ATR(h, lo, c, 14),
		"wilder_rsi_2":         WilderRSI(c, 2),
		"wilder_rsi_14":        WilderRSI(c, 14),
	}
	for name, s := range got {
		want := col(erows, eh, name)
		for i := range want {
			if math.IsNaN(want[i]) != !s.Defined[i] {
				t.Fatalf("%s bar %d: Python undefined %v, Go defined %v", name, i, math.IsNaN(want[i]), s.Defined[i])
			}
			if s.Defined[i] && !near(s.Value[i], want[i], 1e-9) {
				t.Fatalf("%s bar %d: Go %.17g, Python %.17g", name, i, s.Value[i], want[i])
			}
		}
	}
}

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
	return rows
}
