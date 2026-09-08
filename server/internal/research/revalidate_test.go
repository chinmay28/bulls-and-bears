package research

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/sched"
	"github.com/chinmay28/bulls-and-bears/server/internal/spec"
)

// installSpec writes a spec the loader accepts, with the given strategy,
// universe, training cutoff and generation date.
func installSpec(t *testing.T, dir, name, strategy string, universe []string, trainTo, generated string) {
	t.Helper()
	params := map[string]string{
		"ratio_reversion":      "  lookback: 20\n  entry_z: 2\n  exit_z: 0.5\n  max_hold_days: 8\n",
		"sma_trend":            "  lookback: 200\n  band: 0.02\n",
		"dual_momentum":        "  lookback: 252\n  top_k: 1\n  rebalance_days: 21\n",
		"pairs_zscore":         "  hedge_ratio: 1.6\n  lookback: 20\n  entry_z: 2\n  exit_z: 0.5\n  max_hold_days: 30\n",
		"time_series_momentum": "  lookback: 252\n  rebalance_days: 21\n",
		"donchian_breakout":    "  entry_lookback: 55\n  exit_lookback: 20\n",
		"risk_parity_trend":    "  trend_lookback: 200\n  vol_lookback: 63\n  rebalance_days: 21\n",
		"rsi2_reversion":       "  trend_lookback: 200\n  rsi_entry: 5\n  rsi_exit: 70\n  max_hold_days: 5\n",
	}[strategy]
	src := "name: " + name + "\nversion: 1\nstrategy: " + strategy + "\nuniverse: [" + strings.Join(universe, ", ") + "]\n" +
		"params:\n" + params +
		"sizing:\n  gross_leverage: 1\n  max_notional_per_leg_usd: 2000\n" +
		"provenance:\n  train_window: {from: 2005-01-03, to: " + trainTo + "}\n  test_window: {from: 2020-01-02, to: 2026-09-04}\n" +
		"  oos_sharpe: 1.2\n  oos_max_drawdown: -0.1\n  cost_model: {commission_usd: 0, slippage_bps: 5}\n" +
		"  research_git_sha: abc1234\n  generated_at: " + generated + "\n  ttl_days: 90\n"
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newSchedule(t *testing.T, now time.Time) (*Schedule, *Runner) {
	t.Helper()
	r := newRunner(t, true)
	r.SpecsDir = t.TempDir()
	if err := os.MkdirAll(r.SpecsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	s := &Schedule{Runner: r, SpecsDir: r.SpecsDir, DataDir: r.DataDir, Now: func() time.Time { return now }}
	return s, r
}

func TestMarketClosed(t *testing.T) {
	ny := sched.NewYork
	cases := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"saturday", time.Date(2026, 9, 5, 12, 0, 0, 0, ny), true},
		{"weekday before the open", time.Date(2026, 9, 8, 8, 0, 0, 0, ny), true},
		{"weekday at the open", time.Date(2026, 9, 8, 9, 30, 0, 0, ny), false},
		{"weekday mid-session", time.Date(2026, 9, 8, 13, 0, 0, 0, ny), false},
		{"weekday just after the close", time.Date(2026, 9, 8, 16, 10, 0, 0, ny), false},
		{"weekday an hour after the close", time.Date(2026, 9, 8, 17, 0, 0, 0, ny), true},
		{"holiday", time.Date(2026, 9, 7, 12, 0, 0, 0, ny), true}, // Labor Day
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MarketClosed(tc.at); got != tc.want {
				t.Errorf("MarketClosed(%s) = %v, want %v", tc.at, got, tc.want)
			}
		})
	}
}

func TestForSpec(t *testing.T) {
	dir := t.TempDir()
	installSpec(t, dir, "etf_gld_ratio", "ratio_reversion", []string{"SPY", "QQQ", "GLD"}, "2019-12-31", "2026-09-08T00:00:00Z")
	installSpec(t, dir, "gld_gdx_pairs", "pairs_zscore", []string{"GLD", "GDX"}, "2022-12-31", "2026-09-08T00:00:00Z")
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	// No strategy is registered in this package's tests, so the loader
	// refuses every spec; the refusal carries the parsed spec, which is all
	// re-validation needs.
	for _, l := range spec.LoadDir(dir, now) {
		sp := l.Spec
		var refused *spec.Refused
		if errors.As(l.Err, &refused) {
			sp = refused.Spec
		}
		if sp == nil {
			t.Fatalf("%s: %v", l.Path, l.Err)
		}
		st, opts, err := ForSpec(sp)
		if err != nil {
			t.Fatal(err)
		}
		switch sp.Name {
		case "etf_gld_ratio":
			if st.Name != "etf_gld_ratio" || opts.TrainTo != "2019-12-31" || strings.Join(opts.Universe, ",") != "SPY,QQQ,GLD" {
				t.Errorf("etf_gld_ratio -> %s %+v", st.Name, opts)
			}
		case "gld_gdx_pairs":
			// The pairs study has a fixed split; the cutoff is not passed.
			if st.Name != "gld_gdx_pairs" || opts.TrainTo != "" || strings.Join(opts.Universe, ",") != "GLD,GDX" {
				t.Errorf("gld_gdx_pairs -> %s %+v", st.Name, opts)
			}
		}
	}
	if _, _, err := ForSpec(&spec.Spec{Strategy: "momentum_of_the_week"}); err == nil {
		t.Error("an unknown strategy found a study")
	}
	// A spec that names its study is re-validated by that study, not by the
	// first study of its strategy.
	named := &spec.Spec{Strategy: "dual_momentum", Universe: []string{"XLK", "XLE", "GLD"}}
	named.Provenance.ResearchStudy = "sector_rotation"
	named.Provenance.TrainWindow.To = time.Date(2019, 12, 31, 0, 0, 0, 0, time.UTC)
	st, opts, err := ForSpec(named)
	if err != nil || st.Name != "sector_rotation" || opts.TrainTo != "2019-12-31" {
		t.Errorf("named study -> %s %+v %v", st.Name, opts, err)
	}
	named.Provenance.ResearchStudy = ""
	if st, _, err := ForSpec(named); err != nil || st.Name != "dual_momentum" {
		t.Errorf("unnamed dual_momentum -> %s %v", st.Name, err)
	}
	named.Provenance.ResearchStudy = "sma_trend"
	if _, _, err := ForSpec(named); err == nil {
		t.Error("a study that emits another strategy was accepted")
	}
	named.Provenance.ResearchStudy = "no_such_study"
	if _, _, err := ForSpec(named); err == nil {
		t.Error("an unknown study was accepted")
	}
	if got := StudiesForStrategy("dual_momentum"); len(got) != 2 || got[0].Name != "dual_momentum" || got[1].Name != "sector_rotation" {
		t.Errorf("StudiesForStrategy(dual_momentum) = %v", got)
	}
}

func TestPassRevalidatesWhatIsDueOnceAMonth(t *testing.T) {
	// A Saturday: the market is closed.
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	s, r := newSchedule(t, now)
	installSpec(t, s.SpecsDir, "sma_trend", "sma_trend", []string{"SPY", "GLD"}, "2019-12-31", "2026-09-01T00:00:00Z")
	// An expired spec is still installed, and is exactly what needs re-validating.
	installSpec(t, s.SpecsDir, "etf_gld_ratio", "ratio_reversion", []string{"SPY", "GLD"}, "2019-12-31", "2026-01-01T00:00:00Z")
	// One whose cutoff is too recent for the runner: recorded as failed, not retried this month.
	installSpec(t, s.SpecsDir, "fresh", "sma_trend", []string{"QQQ", "GLD"}, "2026-06-30", "2026-09-01T00:00:00Z")

	if v := s.View(); !v.Enabled || len(v.Due) != 3 || v.Running {
		t.Fatalf("view before = %+v", v)
	}
	if ran := s.Pass(context.Background(), false); ran != 3 {
		t.Fatalf("ran %d, want 3", ran)
	}
	v := s.View()
	if len(v.Due) != 0 || len(v.History) != 3 {
		t.Fatalf("view after = %+v", v)
	}
	byName := map[string]Record{}
	for _, rec := range v.History {
		byName[rec.Name] = rec
	}
	if rec := byName["sma_trend"]; rec.Status != Succeeded || rec.Outcome == nil || rec.Outcome.Promoted || rec.Study != "sma_trend" || rec.JobID == "" {
		t.Errorf("sma_trend = %+v", rec)
	}
	if rec := byName["etf_gld_ratio"]; rec.Status != Succeeded || rec.Study != "etf_gld_ratio" {
		t.Errorf("etf_gld_ratio = %+v", rec)
	}
	if rec := byName["fresh"]; rec.Status != Failed || !strings.Contains(rec.Error, "out of sample") {
		t.Errorf("fresh = %+v", rec)
	}
	// The study was pointed at the spec's own universe and cutoff.
	_, text, _ := r.Get(byName["sma_trend"].JobID, 0)
	if !strings.Contains(text, "--universe SPY,GLD") || !strings.Contains(text, "--train-to 2019-12-31") {
		t.Errorf("log lacks the spec's universe or cutoff:\n%s", text)
	}
	// Same month: nothing is due; next month: everything is.
	if ran := s.Pass(context.Background(), false); ran != 0 {
		t.Errorf("second pass ran %d", ran)
	}
	s.Now = func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) }
	if v := s.View(); len(v.Due) != 3 {
		t.Errorf("next month due = %v", v.Due)
	}
	// The state survived on disk.
	s2 := &Schedule{Runner: r, SpecsDir: s.SpecsDir, DataDir: s.DataDir, Now: s.Now}
	if v := s2.View(); len(v.History) != 3 || len(v.LastRun) != 3 {
		t.Errorf("reloaded view = %+v", v)
	}
}

func TestPassWaitsForTheMarketAndTheSwitch(t *testing.T) {
	open := time.Date(2026, 9, 8, 13, 0, 0, 0, sched.NewYork) // a Tuesday, mid-session
	s, _ := newSchedule(t, open)
	installSpec(t, s.SpecsDir, "sma_trend", "sma_trend", []string{"SPY", "GLD"}, "2019-12-31", "2026-09-01T00:00:00Z")
	if ran := s.Pass(context.Background(), false); ran != 0 {
		t.Errorf("ran %d during the session", ran)
	}
	closed := time.Date(2026, 9, 8, 18, 0, 0, 0, sched.NewYork)
	s.Now = func() time.Time { return closed }
	if err := s.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if ran := s.Pass(context.Background(), false); ran != 0 {
		t.Errorf("ran %d while off", ran)
	}
	// Force ignores both.
	s.Now = func() time.Time { return open }
	if ran := s.Pass(context.Background(), true); ran != 1 {
		t.Errorf("forced pass ran %d", ran)
	}
	if err := s.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if !s.View().Enabled {
		t.Error("not enabled after SetEnabled(true)")
	}
}

func TestServeStopsWithTheContext(t *testing.T) {
	s, _ := newSchedule(t, time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC))
	installSpec(t, s.SpecsDir, "sma_trend", "sma_trend", []string{"SPY", "GLD"}, "2019-12-31", "2026-09-01T00:00:00Z")
	ctx, cancel := context.WithCancel(context.Background())
	wakes := 0
	s.Sleep = func(ctx context.Context, d time.Duration) bool {
		wakes++
		if wakes == 2 {
			cancel()
		}
		return ctx.Err() == nil
	}
	s.Serve(ctx)
	if wakes != 2 || len(s.View().History) != 1 {
		t.Errorf("wakes %d, history %d", wakes, len(s.View().History))
	}
}

func TestWait(t *testing.T) {
	r := newRunner(t, true)
	v, err := r.Start(Setup, "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	done, ok := r.Wait(context.Background(), v.ID)
	if !ok || done.Status != Succeeded {
		t.Errorf("Wait = %+v, %v", done, ok)
	}
	if _, ok := r.Wait(context.Background(), "nope"); ok {
		t.Error("Wait found a job that does not exist")
	}
}
