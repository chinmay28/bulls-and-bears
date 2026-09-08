package bars

import (
	"testing"
	"time"
)

// The same 2-for-1 split as research/tests/test_execution.py: raw prices
// halve between bars 1 and 2, adjclose does not, and the adjusted open,
// high and low are continuous across it.
func TestAdjustedOHLCAcrossASplit(t *testing.T) {
	day := func(i int) time.Time { return time.Date(2024, 1, 1+i, 0, 0, 0, 0, time.UTC) }
	series := []Bar{
		{Date: day(0), Open: 200, High: 210, Low: 198, Close: 204, AdjClose: 102},
		{Date: day(1), Open: 204, High: 208, Low: 200, Close: 206, AdjClose: 103},
		{Date: day(2), Open: 101, High: 105, Low: 100, Close: 104, AdjClose: 104},
		{Date: day(3), Open: 103, High: 106, Low: 101, Close: 105, AdjClose: 105},
	}
	wantF := []float64{0.5, 0.5, 1, 1}
	wantO := []float64{100, 102, 101, 103}
	wantH := []float64{105, 104, 105, 106}
	wantL := []float64{99, 100, 100, 101}
	for i, b := range series {
		if f := AdjustFactor(b); f != wantF[i] {
			t.Errorf("bar %d: factor %v, want %v", i, f, wantF[i])
		}
		if o := AdjustedOpen(b); o != wantO[i] {
			t.Errorf("bar %d: open %v, want %v", i, o, wantO[i])
		}
		if h := AdjustedHigh(b); h != wantH[i] {
			t.Errorf("bar %d: high %v, want %v", i, h, wantH[i])
		}
		if l := AdjustedLow(b); l != wantL[i] {
			t.Errorf("bar %d: low %v, want %v", i, l, wantL[i])
		}
	}
}

func TestAdjustFactorRefusesANonPositiveClose(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic for a zero close")
		}
	}()
	AdjustFactor(Bar{Date: time.Now(), Open: 1, Close: 0, AdjClose: 1})
}
