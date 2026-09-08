package sleeves

import (
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
)

func TestUniverse(t *testing.T) {
	risk, haven, err := Universe("x", []string{"SPY", "QQQ", "GLD"})
	if err != nil || haven != "GLD" || strings.Join(risk, ",") != "SPY,QQQ" {
		t.Fatalf("got %v %q %v", risk, haven, err)
	}
	for _, u := range [][]string{{"GLD"}, {"SPY", "SPY", "GLD"}, {"", "GLD"}, nil} {
		if _, _, err := Universe("x", u); err == nil {
			t.Errorf("%v: want an error", u)
		}
	}
}

func TestKnownAndWholeNumber(t *testing.T) {
	if err := Known("x", strategy.Params{"a": 1, "b": 2}, "a", "b"); err != nil {
		t.Fatal(err)
	}
	if err := Known("x", strategy.Params{"a": 1, "c": 2}, "a", "b"); err == nil || !strings.Contains(err.Error(), "unknown params c") {
		t.Errorf("err = %v", err)
	}
	if err := Known("x", strategy.Params{"a": 1}, "a", "b"); err == nil || !strings.Contains(err.Error(), "missing params b") {
		t.Errorf("err = %v", err)
	}
	if v, err := WholeNumber("x", "n", 3, 1); err != nil || v != 3 {
		t.Errorf("got %d %v", v, err)
	}
	for _, v := range []float64{2.5, 0, -1} {
		if _, err := WholeNumber("x", "n", v, 1); err == nil {
			t.Errorf("%v: want an error", v)
		}
	}
}

func TestAlignDropsDatesAnySymbolLacks(t *testing.T) {
	day := func(i int) time.Time { return time.Date(2024, 1, 1+i, 0, 0, 0, 0, time.UTC) }
	hist := map[string][]bars.Bar{
		"A": {{Date: day(0), AdjClose: 1, Close: 1}, {Date: day(1), AdjClose: 2, Close: 2}, {Date: day(2), AdjClose: 3, Close: 3}},
		"H": {{Date: day(0), AdjClose: 10, Close: 10}, {Date: day(2), AdjClose: 30, Close: 30}},
	}
	dates, px, err := Align("x", []string{"A", "H"}, "H", hist)
	if err != nil || len(dates) != 2 || px["A"][1] != 3 || px["H"][1] != 30 {
		t.Fatalf("dates %v px %v err %v", dates, px, err)
	}
	if _, _, err := Align("x", []string{"A", "B"}, "B", hist); err == nil {
		t.Error("want an error for a missing symbol")
	}
	hist["H"] = append(hist["H"], bars.Bar{Date: day(1), AdjClose: 20, Close: 20})
	if _, _, err := Align("x", []string{"A", "H"}, "H", hist); err == nil {
		t.Error("want an error for out-of-order haven bars")
	}
}
