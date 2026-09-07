package refill

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
)

var now = time.Date(2026, 9, 4, 18, 0, 0, 0, time.UTC)

// stub is a Fetcher that answers from a table and records what it was asked.
type stub struct {
	series map[string][]bars.Bar
	err    map[string]error
	asked  map[string][2]time.Time
}

func (s *stub) Daily(_ context.Context, symbol string, from, to time.Time) ([]bars.Bar, error) {
	if s.asked == nil {
		s.asked = map[string][2]time.Time{}
	}
	s.asked[symbol] = [2]time.Time{from, to}
	if err := s.err[symbol]; err != nil {
		return nil, err
	}
	return s.series[symbol], nil
}

// makeBars builds n consecutive daily bars ending the day before now.
func makeBars(n int, source string) []bars.Bar {
	out := make([]bars.Bar, n)
	start := now.Truncate(24*time.Hour).AddDate(0, 0, -n)
	for i := range out {
		out[i] = bars.Bar{
			Date: start.AddDate(0, 0, i), Open: 100, High: 101, Low: 99, Close: 100, AdjClose: 100,
			Volume: 1000, Source: source, FetchedAt: now,
		}
	}
	return out
}

func dir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	return d
}

func write(t *testing.T, d, symbol string, series []bars.Bar) {
	t.Helper()
	if err := bars.Write(filepath.Join(d, symbol+".parquet"), series); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, d, symbol string) []bars.Bar {
	t.Helper()
	series, err := bars.Read(filepath.Join(d, symbol+".parquet"))
	if err != nil {
		t.Fatal(err)
	}
	return series
}

func run(t *testing.T, f Fetcher, d string, symbols ...string) []Result {
	t.Helper()
	return Run(context.Background(), f, symbols, Options{Dir: d, Now: func() time.Time { return now }})
}

func TestRunWritesASymbolThatHadNoFile(t *testing.T) {
	d := dir(t)
	f := &stub{series: map[string][]bars.Bar{"GLD": makeBars(500, "yahoo")}}
	got := run(t, f, d, "GLD")

	if len(got) != 1 || got[0].Error != "" {
		t.Fatalf("results = %+v, want one clean result", got)
	}
	if got[0].Bars != 500 || got[0].Added != 500 {
		t.Errorf("bars = %d added = %d, want 500 and 500", got[0].Bars, got[0].Added)
	}
	if n := len(read(t, d, "GLD")); n != 500 {
		t.Errorf("file holds %d bars, want 500", n)
	}
	// With no file to learn from, the reach back is the lookback.
	wantFrom := day(now.Add(-DefaultLookback))
	if asked := f.asked["GLD"]; !asked[0].Equal(wantFrom) || !asked[1].Equal(day(now)) {
		t.Errorf("asked for %v, want %s to %s", asked, wantFrom.Format("2006-01-02"), day(now).Format("2006-01-02"))
	}
}

func TestRunKeepsTheDepthAFileAlreadyHas(t *testing.T) {
	d := dir(t)
	existing := makeBars(500, "yahoo")
	write(t, d, "GLD", existing)
	f := &stub{series: map[string][]bars.Bar{"GLD": makeBars(503, "yahoo")}}

	got := run(t, f, d, "GLD")
	if got[0].Error != "" {
		t.Fatalf("error = %q, want none", got[0].Error)
	}
	if got[0].Added != 3 {
		t.Errorf("added = %d, want 3", got[0].Added)
	}
	// The range asked for starts at the file's own first bar, not the
	// default lookback: refilling must not quietly shorten history.
	if asked := f.asked["GLD"]; !asked[0].Equal(day(existing[0].Date)) {
		t.Errorf("asked from %s, want the file's first bar %s",
			asked[0].Format("2006-01-02"), existing[0].Date.Format("2006-01-02"))
	}
}

func TestRunLeavesTheFileAloneWhenTheFetchIsWorse(t *testing.T) {
	tests := []struct {
		name    string
		answer  []bars.Bar
		err     error
		wantErr string
	}{
		{name: "the fetch failed", err: errors.New("http 500"), wantErr: "http 500"},
		{name: "nothing came back", answer: []bars.Bar{}, wantErr: "no bars returned"},
		{name: "fewer bars than the file", answer: makeBars(10, "yahoo"), wantErr: "refused"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := dir(t)
			write(t, d, "GLD", makeBars(500, "yahoo"))
			f := &stub{
				series: map[string][]bars.Bar{"GLD": tt.answer},
				err:    map[string]error{"GLD": tt.err},
			}
			got := run(t, f, d, "GLD")

			if got[0].Error == "" {
				t.Fatal("want an error reported")
			}
			if !strings.Contains(got[0].Error, tt.wantErr) {
				t.Errorf("error = %q, want it to mention %q", got[0].Error, tt.wantErr)
			}
			if n := len(read(t, d, "GLD")); n != 500 {
				t.Errorf("file holds %d bars, want the original 500 untouched", n)
			}
			// The screen still shows what is really on disk.
			if got[0].Bars != 500 || got[0].Added != 0 {
				t.Errorf("result = %+v, want the file's own 500 bars and nothing added", got[0])
			}
		})
	}
}

func TestRunReplacesAnUnreadableFile(t *testing.T) {
	d := dir(t)
	path := filepath.Join(d, "GLD.parquet")
	if err := os.WriteFile(path, []byte("not parquet"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &stub{series: map[string][]bars.Bar{"GLD": makeBars(500, "yahoo")}}

	got := run(t, f, d, "GLD")
	if got[0].Error != "" {
		t.Fatalf("error = %q: a refill is how a corrupt file gets replaced", got[0].Error)
	}
	if n := len(read(t, d, "GLD")); n != 500 {
		t.Errorf("file holds %d bars, want 500", n)
	}
}

func TestRunReportsEverySymbolIndependently(t *testing.T) {
	d := dir(t)
	f := &stub{
		series: map[string][]bars.Bar{"GLD": makeBars(500, "yahoo")},
		err:    map[string]error{"GDX": errors.New("delisted")},
	}
	got := run(t, f, d, "GLD", "GDX")

	if len(got) != 2 {
		t.Fatalf("results = %d, want one per symbol", len(got))
	}
	if got[0].Symbol != "GLD" || got[0].Error != "" {
		t.Errorf("first = %+v, want GLD clean and in the order asked", got[0])
	}
	if got[1].Symbol != "GDX" || got[1].Error == "" {
		t.Errorf("second = %+v, want GDX reporting its failure", got[1])
	}
	if _, err := bars.Read(filepath.Join(d, "GLD.parquet")); err != nil {
		t.Errorf("GLD was not written even though its own fetch worked: %v", err)
	}
}

func TestRunCreatesTheDirectory(t *testing.T) {
	d := filepath.Join(dir(t), "bars")
	f := &stub{series: map[string][]bars.Bar{"GLD": makeBars(10, "yahoo")}}
	if got := run(t, f, d, "GLD"); got[0].Error != "" {
		t.Fatalf("error = %q, want the directory created", got[0].Error)
	}
	if n := len(read(t, d, "GLD")); n != 10 {
		t.Errorf("file holds %d bars, want 10", n)
	}
}

// TestRunRefusesASymbolThatIsNotOne pins the reason the pattern is checked
// here: the symbol becomes a filename, and the list can come from a request.
func TestRunRefusesASymbolThatIsNotOne(t *testing.T) {
	for _, symbol := range []string{"../escape", "a/b", "", "gld", "TOOLONGSYMBOL", "GLD;rm"} {
		t.Run(symbol, func(t *testing.T) {
			d := dir(t)
			f := &stub{series: map[string][]bars.Bar{symbol: makeBars(10, "yahoo")}}
			got := run(t, f, d, symbol)
			if got[0].Error == "" {
				t.Errorf("Run(%q) reported no error", symbol)
			}
			if _, asked := f.asked[symbol]; asked {
				t.Errorf("Run(%q) went to the network anyway", symbol)
			}
			entries, _ := os.ReadDir(d)
			if len(entries) != 0 {
				t.Errorf("files written: %d", len(entries))
			}
		})
	}
}
