package runner

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/broker/paper"
	"github.com/chinmay28/bulls-and-bears/server/internal/halt"
	"github.com/chinmay28/bulls-and-bears/server/internal/journal"
	"github.com/chinmay28/bulls-and-bears/server/internal/marketdata"
	"github.com/chinmay28/bulls-and-bears/server/internal/marketdata/replay"
	"github.com/chinmay28/bulls-and-bears/server/internal/risk"
	_ "github.com/chinmay28/bulls-and-bears/server/internal/strategy/pairs"
)

const golden = "../../../golden/gld_gdx"

// fixture lays out a data directory, a specs directory with the golden spec
// re-dated to today, and a bars directory holding the golden bars shifted
// so the last bar is yesterday: a runner that is happy with it has nothing
// stale to refuse. now is the run's clock.
type fixture struct {
	data, specs, barsDir string
	now                  time.Time
	feed                 marketdata.MarketData
	book                 *paper.Book
}

func setup(t *testing.T, edit func(series map[string][]bars.Bar)) *fixture {
	t.Helper()
	if _, err := os.Stat(golden); err != nil {
		t.Skip("no golden directory")
	}
	root := t.TempDir()
	f := &fixture{data: filepath.Join(root, "data"), specs: filepath.Join(root, "specs"), barsDir: filepath.Join(root, "bars")}
	os.MkdirAll(f.specs, 0o700)
	os.MkdirAll(f.barsDir, 0o700)

	series := map[string][]bars.Bar{}
	var last time.Time
	for _, s := range []string{"GLD", "GDX"} {
		b, err := bars.Read(filepath.Join(golden, "bars_"+s+".parquet"))
		if err != nil {
			t.Fatal(err)
		}
		series[s] = b
		last = b[len(b)-1].Date
	}
	// Shift every date forward so the series ends the day before "now".
	f.now = time.Date(2026, 9, 4, 19, 50, 0, 0, time.UTC) // a Friday
	shift := f.now.Truncate(24*time.Hour).AddDate(0, 0, -1).Sub(last)
	for s := range series {
		for i := range series[s] {
			series[s][i].Date = series[s][i].Date.Add(shift)
		}
	}
	if edit != nil {
		edit(series)
	}
	for s, b := range series {
		if err := bars.Write(filepath.Join(f.barsDir, s+".parquet"), b); err != nil {
			t.Fatal(err)
		}
	}

	raw, err := os.ReadFile(filepath.Join(golden, "spec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	specText := regenerated(string(raw), f.now)
	if err := os.WriteFile(filepath.Join(f.specs, "gld_gdx_pairs.yaml"), []byte(specText), 0o600); err != nil {
		t.Fatal(err)
	}

	f.feed = &replay.Source{Dir: f.barsDir, SpreadBps: 4, Now: func() time.Time { return f.now }}
	book, err := paper.Open(filepath.Join(f.data, "bnb.db"), f.feed, paper.Options{StartingCash: 10_000, Costs: paper.Costs{SlippageBps: 5}, Clock: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { book.Close() })
	f.book = book
	return f
}

// regenerated rewrites the spec's generated_at line to the day of the run so
// the TTL is fresh whatever the golden's date was.
func regenerated(spec string, now time.Time) string {
	var out []string
	for _, line := range strings.Split(spec, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "generated_at:") {
			line = "  generated_at: '" + now.Add(-24*time.Hour).UTC().Format("2006-01-02T15:04:05Z") + "'"
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func (f *fixture) runner() *Runner {
	return &Runner{
		Cfg:    Config{DataDir: f.data, SpecsDir: f.specs, BarsDir: f.barsDir, Mode: "dry-run", Risk: risk.DefaultConfig()},
		Quotes: f.feed, Broker: f.book, Books: f.book,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return f.now },
	}
}

func events(t *testing.T, f *fixture, runID string) []journal.Event {
	t.Helper()
	evs, err := journal.Read(journal.File(JournalDir(f.data), runID))
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Check(evs); err != nil {
		t.Errorf("journal invariant: %v", err)
	}
	return evs
}

func kinds(evs []journal.Event) map[journal.Kind]int {
	m := map[journal.Kind]int{}
	for _, e := range evs {
		m[e.Kind]++
	}
	return m
}

func TestRunEndToEndOnPaper(t *testing.T) {
	f := setup(t, nil)
	out, err := f.runner().Run(context.Background(), "2026-09-04")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "completed" {
		t.Fatalf("outcome = %+v", out)
	}
	if len(out.Strategies) != 1 || !out.Strategies[0].Armed || out.Strategies[0].Name != "gld_gdx_pairs" {
		t.Errorf("strategies = %+v", out.Strategies)
	}
	evs := events(t, f, "2026-09-04")
	k := kinds(evs)
	if k[journal.KindQuote] != 2 || k[journal.KindBars] != 2 || k[journal.KindTargets] != 1 {
		t.Errorf("event kinds = %v", k)
	}
	if k[journal.KindRiskDecision] != len(kindsOf(evs, journal.KindOrderSubmitted))+out.Rejected {
		t.Errorf("decisions %d, orders %d, rejected %d", k[journal.KindRiskDecision], k[journal.KindOrderSubmitted], out.Rejected)
	}
	if k[journal.KindFill] != out.Orders {
		t.Errorf("fills %d, orders %d", k[journal.KindFill], out.Orders)
	}
	if !Done(f.data, "2026-09-04") || Done(f.data, "2026-09-05") {
		t.Error("Done should reflect the journal")
	}
	// Whatever the signal said, the book must agree with the journal.
	pos, _ := f.book.Positions(context.Background())
	if out.Orders > 0 && len(pos) == 0 {
		t.Error("orders were placed but the book holds nothing")
	}
	if out.Orders == 0 && len(pos) != 0 {
		t.Error("no orders but the book holds something")
	}
}

func kindsOf(evs []journal.Event, k journal.Kind) []journal.Event {
	var out []journal.Event
	for _, e := range evs {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

func TestRunRefusesStaleBars(t *testing.T) {
	f := setup(t, func(series map[string][]bars.Bar) {
		for s := range series {
			series[s] = series[s][:len(series[s])-10]
		}
	})
	out, err := f.runner().Run(context.Background(), "2026-09-04")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "failed" || !strings.Contains(out.Reason, "stale") {
		t.Fatalf("outcome = %+v, want a stale-bars failure", out)
	}
	k := kinds(events(t, f, "2026-09-04"))
	if k[journal.KindOrderSubmitted] != 0 || k[journal.KindError] != 1 {
		t.Errorf("events = %v: a failed run must place nothing", k)
	}
}

func TestRunHonoursTheHaltMarker(t *testing.T) {
	f := setup(t, nil)
	if err := halt.Halt(f.data, halt.Marker{Reason: "holiday", By: "cli"}); err != nil {
		t.Fatal(err)
	}
	out, _ := f.runner().Run(context.Background(), "2026-09-04")
	if out.Status != "halted" || !strings.Contains(out.Reason, "holiday") {
		t.Fatalf("outcome = %+v", out)
	}
	k := kinds(events(t, f, "2026-09-04"))
	if k[journal.KindHalt] != 1 || k[journal.KindQuote] != 0 {
		t.Errorf("events = %v: a halted run asks for nothing", k)
	}
}

func TestRunRefusesWithNoArmedSpec(t *testing.T) {
	f := setup(t, nil)
	os.Remove(filepath.Join(f.specs, "gld_gdx_pairs.yaml"))
	os.WriteFile(filepath.Join(f.specs, "junk.yaml"), []byte("name: x\n"), 0o600)
	out, _ := f.runner().Run(context.Background(), "2026-09-04")
	if out.Status != "failed" || !strings.Contains(out.Reason, "no armed strategy") {
		t.Fatalf("outcome = %+v", out)
	}
	if len(out.Strategies) != 1 || out.Strategies[0].Armed || out.Strategies[0].Reason == "" {
		t.Errorf("strategies = %+v, want the junk file listed with its reason", out.Strategies)
	}
}

func TestKillSwitchWritesTheMarker(t *testing.T) {
	f := setup(t, nil)
	// Mark the book high, then have it lose 11% before the run.
	ctx := context.Background()
	f.book.Equity(ctx) // opens equity_history at 10,000 on 2026-09-04
	high := f.now.AddDate(0, 0, -30)
	book2, err := paper.Open(filepath.Join(f.data, "bnb.db"), f.feed, paper.Options{StartingCash: 10_000, Clock: func() time.Time { return high }})
	if err != nil {
		t.Fatal(err)
	}
	book2.Close()
	// Simplest way to a drawdown on paper: an accounting stub that says so.
	r := f.runner()
	r.Books = highWater{Accounting: f.book, high: 11_500}
	out, _ := r.Run(ctx, "2026-09-04")
	if out.Status != "halted" || !strings.Contains(out.Reason, "kill switch") {
		t.Fatalf("outcome = %+v", out)
	}
	if !halt.Halted(f.data) {
		t.Error("the kill switch must write the halt marker")
	}
	m, _ := halt.Status(f.data)
	if m.By != "risk gate" {
		t.Errorf("marker by %q", m.By)
	}
}

type highWater struct {
	Accounting
	high float64
}

func (h highWater) HighWaterEquity(context.Context) (float64, error) { return h.high, nil }

func TestSecondRunOfTheDayAppendsAndIsDone(t *testing.T) {
	f := setup(t, nil)
	r := f.runner()
	r.Run(context.Background(), "2026-09-04")
	before := len(events(t, f, "2026-09-04"))
	r.Run(context.Background(), "2026-09-04")
	after := len(events(t, f, "2026-09-04"))
	if after <= before {
		t.Error("a second run must append, never truncate")
	}
}
