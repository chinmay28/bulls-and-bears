package bars

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
)

func d(y int, m time.Month, day int) time.Time {
	return time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
}

// good is a valid three-bar series: Tue–Thu, 2–4 January 2024.
func good() []Bar {
	fetched := time.Date(2024, 1, 5, 12, 30, 0, 123456000, time.UTC)
	return []Bar{
		{Date: d(2024, 1, 2), Open: 100, High: 102, Low: 99, Close: 101, AdjClose: 100.5, Volume: 1000, Source: "yahoo", FetchedAt: fetched},
		{Date: d(2024, 1, 3), Open: 101.5, High: 104, Low: 101, Close: 103, AdjClose: 102.5, Volume: 2000, Source: "yahoo", FetchedAt: fetched},
		{Date: d(2024, 1, 4), Open: 103, High: 103.5, Low: 101.5, Close: 102, AdjClose: 102, Volume: 1500, Source: "yahoo", FetchedAt: fetched},
	}
}

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SPY.parquet")
	want := good()
	// Round-trip should normalise a non-midnight date and a non-UTC fetch
	// time without changing which day or instant they mean.
	want[1].Date = time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	want[2].FetchedAt = want[2].FetchedAt.In(time.FixedZone("EST", -5*3600))

	if err := Write(path, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file left behind: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d bars, want %d", len(got), len(want))
	}
	for i := range want {
		w, g := want[i], got[i]
		if !g.Date.Equal(w.Date) || g.Date.Location() != time.UTC {
			t.Errorf("bar %d: Date %v, want %v UTC", i, g.Date, w.Date)
		}
		if !g.FetchedAt.Equal(w.FetchedAt) || g.FetchedAt.Location() != time.UTC {
			t.Errorf("bar %d: FetchedAt %v, want %v UTC", i, g.FetchedAt, w.FetchedAt)
		}
		if g.Open != w.Open || g.High != w.High || g.Low != w.Low || g.Close != w.Close || g.AdjClose != w.AdjClose {
			t.Errorf("bar %d: prices %+v, want %+v", i, g, w)
		}
		if g.Volume != w.Volume || g.Source != w.Source {
			t.Errorf("bar %d: volume/source %d %q, want %d %q", i, g.Volume, g.Source, w.Volume, w.Source)
		}
	}
}

func TestWriteRefusesBadSeries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "BAD.parquet")
	bad := good()
	bad[1].Low = 1000
	if err := Write(path, bad); err == nil {
		t.Fatal("Write of a series failing Check succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file written despite failing check: %v", err)
	}
}

// The fixture was written by pandas.to_parquet through pyarrow: columns in a
// different order from §4.1, a __index_level_0__ column from a non-default
// index, source as a dictionary-encoded large_string, fetched_at as
// timestamp[us, tz=UTC]. testdata/make_pyarrow_bars.py is what made it.
func TestReadPyarrowFixture(t *testing.T) {
	got, err := Read("testdata/pyarrow_bars.parquet")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := good()
	want[2].Source = "robinhood"
	if len(got) != len(want) {
		t.Fatalf("got %d bars, want %d", len(got), len(want))
	}
	for i := range want {
		w, g := want[i], got[i]
		if !g.Date.Equal(w.Date) || g.Date.Location() != time.UTC {
			t.Errorf("bar %d: Date %v, want %v", i, g.Date, w.Date)
		}
		if !g.FetchedAt.Equal(w.FetchedAt) {
			t.Errorf("bar %d: FetchedAt %v, want %v", i, g.FetchedAt, w.FetchedAt)
		}
		if g.Open != w.Open || g.High != w.High || g.Low != w.Low || g.Close != w.Close || g.AdjClose != w.AdjClose || g.Volume != w.Volume || g.Source != w.Source {
			t.Errorf("bar %d: got %+v, want %+v", i, g, w)
		}
	}
	if !MixedSources(got) {
		t.Error("fixture mixes yahoo and robinhood; MixedSources said no")
	}
}

func TestReadBadFile(t *testing.T) {
	dir := t.TempDir()
	notParquet := filepath.Join(dir, "junk.parquet")
	if err := os.WriteFile(notParquet, []byte("not a parquet file at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, path string }{
		{"missing", filepath.Join(dir, "nope.parquet")},
		{"not parquet", notParquet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Read(tc.path)
			if err == nil {
				t.Fatalf("Read succeeded with %d bars", len(got))
			}
			if got != nil {
				t.Errorf("Read returned bars alongside an error")
			}
		})
	}
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name  string
		mut   func(s []Bar)
		index int    // expected bar index in the message
		want  string // expected substring naming the invariant
	}{
		{"valid", func([]Bar) {}, -1, ""},
		{"empty is valid", nil, -1, ""},
		{"date unset", func(s []Bar) { s[0].Date = time.Time{} }, 0, "date is unset"},
		{"date repeated", func(s []Bar) { s[2].Date = s[1].Date }, 2, "not after the previous bar's 2024-01-03"},
		{"date backwards", func(s []Bar) { s[1].Date = d(2023, 12, 29) }, 1, "not after the previous bar's 2024-01-02"},
		{"open zero", func(s []Bar) { s[1].Open = 0 }, 1, "open 0 is not positive"},
		{"high negative", func(s []Bar) { s[0].High = -1 }, 0, "high -1 is not positive"},
		{"low NaN", func(s []Bar) { s[2].Low = nan() }, 2, "low NaN is not positive"},
		{"close zero", func(s []Bar) { s[0].Close = 0 }, 0, "close 0 is not positive"},
		{"adjclose zero", func(s []Bar) { s[2].AdjClose = 0 }, 2, "adjclose 0 is not positive"},
		{"low above open", func(s []Bar) { s[1].Low = 102 }, 1, "low 102 is above min(open, close) 101.5"},
		{"low above close", func(s []Bar) { s[2].Low = 102.5 }, 2, "low 102.5 is above min(open, close) 102"},
		{"high below open", func(s []Bar) { s[2].High = 102.5 }, 2, "high 102.5 is below max(open, close) 103"},
		{"high below close", func(s []Bar) { s[1].High = 102.9 }, 1, "high 102.9 is below max(open, close) 103"},
		{"adjclose above close", func(s []Bar) { s[0].AdjClose = 101.01 }, 0, "adjclose 101.01 is above close 101"},
		{"adjclose equal within tolerance", func(s []Bar) { s[0].AdjClose = 101 * (1 + 5e-10) }, -1, ""},
		{"adjclose equal", func(s []Bar) { s[0].AdjClose = 101 }, -1, ""},
		{"first violation wins", func(s []Bar) { s[0].Open = 0; s[2].Low = 999 }, 0, "open 0 is not positive"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var s []Bar
			if tc.mut != nil {
				s = good()
				tc.mut(s)
			}
			err := Check(s)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Check: unexpected error %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Check passed, want an error")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.want) {
				t.Errorf("error %q does not name the invariant %q", msg, tc.want)
			}
			date := s[tc.index].Date.UTC().Format("2006-01-02")
			if s[tc.index].Date.IsZero() {
				date = "0001-01-01"
			}
			if prefix := "bar " + itoa(tc.index) + " (" + date + ")"; !strings.HasPrefix(msg, prefix) {
				t.Errorf("error %q does not start with %q", msg, prefix)
			}
		})
	}
}

func TestReadFailsCheck(t *testing.T) {
	// Write is gated by Check, so build a failing file through the raw row
	// type and make sure Read refuses it rather than handing bars back.
	path := filepath.Join(t.TempDir(), "BAD.parquet")
	bad := good()
	bad[1].AdjClose = bad[1].Close * 2
	writeRaw(t, path, bad)

	got, err := Read(path)
	if err == nil {
		t.Fatalf("Read succeeded with %d bars", len(got))
	}
	if got != nil {
		t.Error("Read returned bars alongside an error")
	}
	if !strings.Contains(err.Error(), "bar 1 (2024-01-03): adjclose") {
		t.Errorf("error %q does not name the bad bar", err)
	}
}

func TestAlign(t *testing.T) {
	bar := func(day int, src string) Bar {
		b := good()[0]
		b.Date = d(2024, 1, day)
		b.Source = src
		return b
	}
	tests := []struct {
		name      string
		in        map[string][]Bar
		wantDates []time.Time
	}{
		{"empty", map[string][]Bar{}, nil},
		{"one symbol", map[string][]Bar{"A": {bar(2, "a"), bar(3, "a")}}, []time.Time{d(2024, 1, 2), d(2024, 1, 3)}},
		{
			"B lacks the middle day",
			map[string][]Bar{
				"A": {bar(2, "a"), bar(3, "a"), bar(4, "a")},
				"B": {bar(2, "b"), bar(4, "b")},
			},
			[]time.Time{d(2024, 1, 2), d(2024, 1, 4)},
		},
		{
			"each lacks a different day",
			map[string][]Bar{
				"A": {bar(2, "a"), bar(3, "a"), bar(5, "a")},
				"B": {bar(3, "b"), bar(4, "b"), bar(5, "b")},
				"C": {bar(2, "c"), bar(3, "c"), bar(4, "c"), bar(5, "c")},
			},
			[]time.Time{d(2024, 1, 3), d(2024, 1, 5)},
		},
		{
			"nothing in common",
			map[string][]Bar{"A": {bar(2, "a")}, "B": {bar(3, "b")}},
			nil,
		},
		{
			"one symbol empty",
			map[string][]Bar{"A": {bar(2, "a")}, "B": {}},
			nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dates, aligned := Align(tc.in)
			if len(dates) != len(tc.wantDates) {
				t.Fatalf("dates %v, want %v", dates, tc.wantDates)
			}
			for i := range dates {
				if !dates[i].Equal(tc.wantDates[i]) {
					t.Errorf("dates[%d] = %v, want %v", i, dates[i], tc.wantDates[i])
				}
			}
			if len(aligned) != len(tc.in) {
				t.Fatalf("aligned has %d symbols, want %d", len(aligned), len(tc.in))
			}
			for sym, bars := range aligned {
				if len(bars) != len(dates) {
					t.Fatalf("%s: %d bars, want %d", sym, len(bars), len(dates))
				}
				for i, b := range bars {
					if !b.Date.Equal(dates[i]) {
						t.Errorf("%s[%d].Date = %v, want %v", sym, i, b.Date, dates[i])
					}
					// The bar must be that symbol's own, not a neighbour's.
					if b.Source != strings.ToLower(sym) {
						t.Errorf("%s[%d] came from %q", sym, i, b.Source)
					}
				}
			}
		})
	}
}

func TestWindow(t *testing.T) {
	s := good() // 2, 3, 4 January
	tests := []struct {
		name     string
		from, to time.Time
		want     []int // days of January expected
	}{
		{"all", d(2024, 1, 1), d(2024, 1, 31), []int{2, 3, 4}},
		{"exact edges inclusive", d(2024, 1, 2), d(2024, 1, 4), []int{2, 3, 4}},
		{"from on a bar", d(2024, 1, 3), d(2024, 1, 31), []int{3, 4}},
		{"to on a bar", d(2024, 1, 1), d(2024, 1, 3), []int{2, 3}},
		{"single day", d(2024, 1, 3), d(2024, 1, 3), []int{3}},
		{"between bars", d(2024, 1, 2).Add(time.Hour), d(2024, 1, 3).Add(-time.Hour), nil},
		{"before all", d(2023, 12, 1), d(2023, 12, 31), nil},
		{"after all", d(2024, 2, 1), d(2024, 2, 28), nil},
		{"inverted", d(2024, 1, 4), d(2024, 1, 2), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Window(s, tc.from, tc.to)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d bars, want %d", len(got), len(tc.want))
			}
			for i, b := range got {
				if b.Date.Day() != tc.want[i] {
					t.Errorf("got[%d] = %v, want day %d", i, b.Date, tc.want[i])
				}
			}
		})
	}
	if Window(nil, d(2024, 1, 1), d(2024, 1, 31)) != nil {
		t.Error("Window of nil is not nil")
	}
}

func TestStale(t *testing.T) {
	fri := d(2024, 1, 5) // Friday
	series := func(last time.Time) []Bar {
		b := good()[0]
		b.Date = last
		return []Bar{b}
	}
	tests := []struct {
		name string
		last time.Time
		now  time.Time
		max  int
		want bool
	}{
		{"same day", fri, fri.Add(20 * time.Hour), 3, false},
		{"Friday bar on Saturday", fri, d(2024, 1, 6), 3, false},
		{"Friday bar on Sunday", fri, d(2024, 1, 7), 3, false},
		{"Friday bar on Monday: one trading day missed", fri, d(2024, 1, 8), 3, false},
		{"Friday bar on Wednesday: three missed", fri, d(2024, 1, 10), 3, false},
		{"Friday bar on Thursday: four missed", fri, d(2024, 1, 11), 3, true},
		{"Friday bar on Thursday with max 4", fri, d(2024, 1, 11), 4, false},
		{"Friday bar two Mondays on", fri, d(2024, 1, 15), 3, true},
		{"Monday bar on Tuesday, zero tolerance", d(2024, 1, 8), d(2024, 1, 9), 0, true},
		{"Friday bar on Sunday, zero tolerance", fri, d(2024, 1, 7), 0, false},
		{"now before the bar", fri, d(2024, 1, 1), 3, false},
		{"now late on the day in a western zone", fri, time.Date(2024, 1, 10, 20, 0, 0, 0, time.FixedZone("PST", -8*3600)), 3, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Stale(series(tc.last), tc.now, tc.max); got != tc.want {
				t.Errorf("Stale(last %s, now %s, max %d) = %v, want %v", tc.last.Format("Mon 2006-01-02"), tc.now.UTC().Format("Mon 2006-01-02 15:04"), tc.max, got, tc.want)
			}
		})
	}
	if !Stale(nil, fri, 3) {
		t.Error("empty series is not stale")
	}
}

func TestMixedSources(t *testing.T) {
	tests := []struct {
		name    string
		sources []string
		want    bool
	}{
		{"empty", nil, false},
		{"one bar", []string{"yahoo"}, false},
		{"all yahoo", []string{"yahoo", "yahoo", "yahoo"}, false},
		{"one robinhood at the end", []string{"yahoo", "yahoo", "robinhood"}, true},
		{"one robinhood at the start", []string{"robinhood", "yahoo", "yahoo"}, true},
		{"blank counts as a source", []string{"yahoo", ""}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := make([]Bar, len(tc.sources))
			for i, src := range tc.sources {
				s[i] = good()[0]
				s[i].Date = d(2024, 1, 2+i)
				s[i].Source = src
			}
			if got := MixedSources(s); got != tc.want {
				t.Errorf("MixedSources(%v) = %v, want %v", tc.sources, got, tc.want)
			}
		})
	}
}

func nan() float64 { return math.NaN() }

func itoa(i int) string { return strconv.Itoa(i) }

// writeRaw writes bars through the on-disk row type with no Check, for tests
// that need a file Read must reject.
func writeRaw(t *testing.T, path string, series []Bar) {
	t.Helper()
	rows := make([]row, len(series))
	for i, b := range series {
		rows[i] = row{Date: int32(day(b.Date).Unix() / 86400), Open: b.Open, High: b.High, Low: b.Low, Close: b.Close, AdjClose: b.AdjClose, Volume: b.Volume, Source: b.Source, FetchedAt: b.FetchedAt}
	}
	if err := parquet.WriteFile(path, rows); err != nil {
		t.Fatal(err)
	}
}
