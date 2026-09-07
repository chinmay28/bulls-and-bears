package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/broker/paper"
	"github.com/chinmay28/bulls-and-bears/server/internal/marketdata/replay"
	"github.com/chinmay28/bulls-and-bears/server/internal/risk"
	"github.com/chinmay28/bulls-and-bears/server/internal/runner"
	_ "github.com/chinmay28/bulls-and-bears/server/internal/strategy/pairs"
)

const golden = "../../../golden/gld_gdx"

// tradingServer stands the API up over the golden spec and bars, shifted so
// the bars end yesterday and the spec is fresh, with a paper book behind it.
func tradingServer(t *testing.T) (*Server, http.Handler, time.Time) {
	t.Helper()
	if _, err := os.Stat(golden); err != nil {
		t.Skip("no golden directory")
	}
	root := t.TempDir()
	data, specs, barsDir := filepath.Join(root, "data"), filepath.Join(root, "specs"), filepath.Join(root, "bars")
	os.MkdirAll(specs, 0o700)
	os.MkdirAll(barsDir, 0o700)
	now := time.Now().UTC()
	// The newest bar is placed on the last weekday before today so it is
	// never stale, whatever day the test runs on.
	last := now.Truncate(24*time.Hour).AddDate(0, 0, -1)
	for last.Weekday() == time.Saturday || last.Weekday() == time.Sunday {
		last = last.AddDate(0, 0, -1)
	}
	var shift time.Duration
	for _, s := range []string{"GLD", "GDX"} {
		series, err := bars.Read(filepath.Join(golden, "bars_"+s+".parquet"))
		if err != nil {
			t.Fatal(err)
		}
		shift = last.Sub(series[len(series)-1].Date)
		for i := range series {
			series[i].Date = series[i].Date.Add(shift)
		}
		if err := bars.Write(filepath.Join(barsDir, s+".parquet"), series); err != nil {
			t.Fatal(err)
		}
	}
	// The spec's test window moves with the bars, so a backtest from the
	// phone opens its book at the same bar the golden did.
	raw, _ := os.ReadFile(filepath.Join(golden, "spec.yaml"))
	var lines []string
	inTest := false
	for _, line := range strings.Split(string(raw), "\n") {
		trim := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trim, "generated_at:"):
			line = "  generated_at: '" + now.Add(-time.Hour).Format("2006-01-02T15:04:05Z") + "'"
		case strings.HasPrefix(trim, "test_window:"):
			inTest = true
		case strings.HasPrefix(trim, "train_window:"):
			inTest = false
		case inTest && (strings.HasPrefix(trim, "from:") || strings.HasPrefix(trim, "to:")):
			key := strings.SplitN(trim, ":", 2)[0]
			d, err := time.Parse("2006-01-02", strings.Trim(strings.TrimSpace(strings.SplitN(trim, ":", 2)[1]), "'\""))
			if err != nil {
				t.Fatal(err)
			}
			line = "    " + key + ": '" + d.Add(shift).Format("2006-01-02") + "'"
		}
		lines = append(lines, line)
	}
	os.WriteFile(filepath.Join(specs, "gld_gdx_pairs.yaml"), []byte(strings.Join(lines, "\n")), 0o600)
	os.WriteFile(filepath.Join(specs, "broken.yaml"), []byte("name: broken\n"), 0o600)

	quotes := &replay.Source{Dir: barsDir, SpreadBps: 4}
	book, err := paper.Open(filepath.Join(data, "bnb.db"), quotes, paper.Options{StartingCash: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { book.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := &runner.Runner{
		Cfg:    runner.Config{DataDir: data, SpecsDir: specs, BarsDir: barsDir, Mode: "dry-run", Risk: risk.DefaultConfig()},
		Quotes: quotes, Broker: book, Books: book, Log: log,
	}
	s := &Server{
		Log: log, Version: "v2026.9.7", DataDir: data, SpecsDir: specs, BarsDir: barsDir, Book: book,
		Risk: risk.DefaultConfig(), RunOffset: 10 * time.Minute, Ref: "main",
		RunNow: func(ctx context.Context, id string) (runner.Outcome, error) { return r.Run(ctx, id) },
	}
	return s, s.Auth.Middleware(s.Routes()), now
}

func TestStrategiesListArmedAndBroken(t *testing.T) {
	_, h, _ := tradingServer(t)
	w := do(t, h, "GET", "/api/strategies", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	list := decode[[]strategyView](t, w)
	if len(list) != 2 {
		t.Fatalf("got %d strategies: %s", len(list), w.Body)
	}
	byName := map[string]strategyView{}
	for _, v := range list {
		byName[v.Name] = v
	}
	armed := byName["gld_gdx_pairs"]
	if armed.Status != "armed" || armed.Signal == nil || armed.Provenance == nil || armed.FreshDays < 80 {
		t.Errorf("armed = %+v", armed)
	}
	if armed.Signal.Stale {
		t.Errorf("signal reads stale: %+v", armed.Signal)
	}
	broken := byName["broken"]
	if broken.Status != "invalid" || broken.Reason == "" || broken.Signal != nil {
		t.Errorf("broken = %+v", broken)
	}

	w = do(t, h, "GET", "/api/strategies/gld_gdx_pairs", "")
	if w.Code != http.StatusOK || decode[strategyView](t, w).Name != "gld_gdx_pairs" {
		t.Errorf("get: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "GET", "/api/strategies/nope", ""); w.Code != http.StatusNotFound {
		t.Errorf("missing: %d", w.Code)
	}
}

func TestBacktestFromThePhone(t *testing.T) {
	_, h, _ := tradingServer(t)
	w := do(t, h, "POST", "/api/strategies/gld_gdx_pairs/backtest", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	v := decode[backtestView](t, w)
	if v.Bars == 0 || len(v.Equity) == 0 || len(v.Equity) > 401 || v.Sharpe == nil {
		t.Errorf("backtest = %+v", v)
	}
	// Same bars, same spec, same engine as the golden: the Sharpe must agree
	// with what the spec recorded.
	if d := *v.Sharpe - v.SpecSharpe; d > 0.05 || d < -0.05 {
		t.Errorf("sharpe %.3f vs spec %.3f", *v.Sharpe, v.SpecSharpe)
	}
}

func TestRunNowThenBookAndRuns(t *testing.T) {
	_, h, _ := tradingServer(t)
	w := do(t, h, "POST", "/api/run", "")
	if w.Code != http.StatusOK {
		t.Fatalf("run: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `"status":"completed"`) {
		t.Fatalf("run did not complete: %s", w.Body)
	}

	w = do(t, h, "GET", "/api/runs", "")
	runs := decode[[]runView](t, w)
	if len(runs) != 1 || runs[0].Status != "completed" || runs[0].Invariant != "" {
		t.Errorf("runs = %+v", runs)
	}
	w = do(t, h, "GET", "/api/runs/"+runs[0].RunID, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"kind":"targets"`) {
		t.Errorf("run detail: %d %s", w.Code, w.Body)
	}
	w = do(t, h, "GET", "/api/runs/"+runs[0].RunID+"/jsonl", "")
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/x-ndjson") {
		t.Errorf("jsonl: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if w := do(t, h, "GET", "/api/runs/2000-01-01", ""); w.Code != http.StatusNotFound {
		t.Errorf("missing run: %d", w.Code)
	}

	w = do(t, h, "GET", "/api/book", "")
	if w.Code != http.StatusOK {
		t.Fatalf("book: %d %s", w.Code, w.Body)
	}
	book := decode[bookView](t, w)
	if book.StartingCash != 10_000 || book.Equity <= 0 || len(book.History) == 0 {
		t.Errorf("book = %+v", book)
	}
	if len(book.Fills) != runs[0].Fills {
		t.Errorf("book has %d fills, run says %d", len(book.Fills), runs[0].Fills)
	}

	w = do(t, h, "GET", "/api/overview", "")
	ov := decode[overviewView](t, w)
	if ov.Book == nil || ov.LastRun == nil || ov.NextRun == nil || len(ov.Strategies) != 2 {
		t.Errorf("overview = %s", w.Body)
	}
	if ov.Book != nil && ov.Book.OrdersToday != runs[0].Orders {
		t.Errorf("orders today = %d, run placed %d", ov.Book.OrdersToday, runs[0].Orders)
	}
}

func TestBarsListing(t *testing.T) {
	_, h, _ := tradingServer(t)
	w := do(t, h, "GET", "/api/bars", "")
	list := decode[[]barsView](t, w)
	if len(list) != 2 || list[0].Bars == 0 || list[0].Stale || list[0].Source == "" {
		t.Errorf("bars = %+v", list)
	}
}

func TestServerWithoutARunnerSaysSo(t *testing.T) {
	_, h := testServer(t, "")
	if w := do(t, h, "POST", "/api/run", ""); w.Code != http.StatusConflict {
		t.Errorf("run: %d", w.Code)
	}
	if w := do(t, h, "GET", "/api/book", ""); w.Code != http.StatusConflict {
		t.Errorf("book: %d", w.Code)
	}
	w := do(t, h, "GET", "/api/overview", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"book":null`) {
		t.Errorf("overview: %d %s", w.Code, w.Body)
	}
}
