// Package runner is the daily cycle: quotes in, targets out, every intent
// through the gate, every order to the broker, everything to the journal.
//
// One Run is one trading day. It loads the specs and refuses the ones it
// must, reads the bars and refuses to trade on stale ones, asks the market
// for quotes, asks each strategy for targets, turns the difference between
// targets and positions into intents, has the risk gate decide each one, and
// sends the allowed ones — journalling each step before the next. Anything
// missing, stale or unreadable ends the run before an order goes out. The
// same code runs in dry-run and live; only the broker behind the interface
// differs, and the runner never asks which it has.
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/broker"
	"github.com/chinmay28/bulls-and-bears/server/internal/halt"
	"github.com/chinmay28/bulls-and-bears/server/internal/journal"
	"github.com/chinmay28/bulls-and-bears/server/internal/marketdata"
	"github.com/chinmay28/bulls-and-bears/server/internal/risk"
	"github.com/chinmay28/bulls-and-bears/server/internal/spec"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
)

// Accounting is what the risk gate needs to know about the account over
// time, beyond what broker.Broker says about it now. The paper book keeps
// it; a live broker adapter can keep it the same way.
type Accounting interface {
	StartOfDayEquity(ctx context.Context, day time.Time) (float64, error)
	HighWaterEquity(ctx context.Context) (float64, error)
	DailyLimitHits(ctx context.Context, before time.Time, limit float64) (int, error)
}

// Config is what a Runner needs to know.
type Config struct {
	DataDir  string // HALT marker and journal/
	SpecsDir string
	BarsDir  string // <SYMBOL>.parquet
	Mode     string // "dry-run" or "live"
	Risk     risk.Config
	// MaxBarAge is how many trading days old the newest bar may be; the
	// plan says three.
	MaxBarAge int
	// MinNotionalUSD is the smallest rebalance worth an order. Below it the
	// difference between target and held is left alone.
	MinNotionalUSD float64
	// AllowMixedSources lets a symbol's history mix sources; off by default,
	// as the plan says.
	AllowMixedSources bool
}

// Runner runs the cycle.
type Runner struct {
	Cfg    Config
	Quotes marketdata.MarketData
	Broker broker.Broker
	Books  Accounting
	Log    *slog.Logger
	// Now is the clock; nil means time.Now. A run stamps its journal with
	// it and judges TTLs and quote ages against it.
	Now func() time.Time
}

// Outcome is what a run came to, for the caller's log and the API.
type Outcome struct {
	RunID      string
	Status     string // completed, halted, failed
	Reason     string
	Orders     int
	Rejected   int
	Equity     float64
	Strategies []StrategyOutcome
}

// StrategyOutcome is one spec's fate in the run.
type StrategyOutcome struct {
	Name    string
	Path    string
	Armed   bool
	Reason  string
	Targets map[string]float64
}

// JournalDir is where a data directory keeps its journals.
func JournalDir(dataDir string) string { return filepath.Join(dataDir, "journal") }

// Run performs one cycle for runID (the trading date). It returns an error
// only when the journal itself cannot be written; every other failure is
// journalled and reported in the Outcome, because a run that fails is still
// a run that happened.
func (r *Runner) Run(ctx context.Context, runID string) (Outcome, error) {
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	log := r.Log
	if log == nil {
		log = slog.Default()
	}
	w, err := journal.Open(JournalDir(r.Cfg.DataDir), runID, r.Cfg.Mode)
	if err != nil {
		return Outcome{}, err
	}
	defer w.Close()
	out := Outcome{RunID: runID, Status: "failed"}
	write := func(ev journal.Event) error { return w.Write(ev) }
	fail := func(reason string) (Outcome, error) {
		out.Reason = reason
		log.Error("run failed", "run_id", runID, "reason", reason)
		if err := write(journal.New(journal.KindError, "", map[string]string{"error": reason})); err != nil {
			return out, err
		}
		return out, write(journal.New(journal.KindRun, "", map[string]string{"status": "failed", "reason": reason}))
	}
	if err := write(journal.New(journal.KindRun, "", map[string]string{"status": "started"})); err != nil {
		return out, err
	}

	// 0. The halt marker, at the moment of the run.
	if m, err := halt.Status(r.Cfg.DataDir); !errors.Is(err, halt.ErrNotHalted) {
		reason := "halted: " + m.Reason
		if err != nil {
			reason = "halted: cannot read the halt marker: " + err.Error()
		}
		out.Status, out.Reason = "halted", reason
		if err := write(journal.New(journal.KindHalt, "", map[string]any{"reason": m.Reason, "by": m.By, "at": m.At})); err != nil {
			return out, err
		}
		return out, write(journal.New(journal.KindRun, "", map[string]string{"status": "halted", "reason": reason}))
	}

	// 1. Specs: every file, armed or refused, on the record. Each one is
	// journalled with its reason, so a run that trades nothing says on the
	// phone which specs it saw and why it refused them.
	loaded := spec.LoadDir(r.Cfg.SpecsDir, now())
	var armed []armedSpec
	for _, l := range loaded {
		so := StrategyOutcome{Path: l.Path}
		if l.Err != nil {
			so.Reason = l.Err.Error()
		} else {
			so.Name = l.Spec.Name
			st, err := strategy.New(l.Spec.Strategy, l.Spec.Universe, l.Spec.Params, l.Spec.Sizing)
			if err != nil {
				so.Reason = err.Error()
			} else {
				so.Armed = true
				armed = append(armed, armedSpec{spec: l.Spec, st: st})
			}
		}
		out.Strategies = append(out.Strategies, so)
		if err := write(journal.New(journal.KindStrategy, "", map[string]any{
			"name": specName(so), "path": filepath.Base(so.Path), "armed": so.Armed, "reason": so.Reason,
		})); err != nil {
			return out, err
		}
	}
	if len(armed) == 0 {
		return fail(noArmedReason(r.Cfg.SpecsDir, out.Strategies))
	}

	// 2. Bars for every symbol any armed strategy needs; stale or mixed is a
	// refusal, not a warning.
	symbols := map[string]bool{}
	for _, a := range armed {
		for _, s := range a.spec.Universe {
			symbols[s] = true
		}
	}
	hist := map[string][]bars.Bar{}
	for s := range symbols {
		series, err := bars.Read(filepath.Join(r.Cfg.BarsDir, s+".parquet"))
		if err != nil {
			return fail("bars: " + err.Error())
		}
		if len(series) == 0 {
			return fail("bars: " + s + " has no bars")
		}
		maxAge := r.Cfg.MaxBarAge
		if maxAge <= 0 {
			maxAge = 3
		}
		if bars.Stale(series, now(), maxAge) {
			return fail(fmt.Sprintf("bars: %s is stale: last bar %s, limit %d trading days", s, series[len(series)-1].Date.Format("2006-01-02"), maxAge))
		}
		if !r.Cfg.AllowMixedSources && bars.MixedSources(series) {
			return fail("bars: " + s + " mixes sources (pass --allow-mixed-sources to accept)")
		}
		hist[s] = series
	}
	for s, series := range hist {
		last := series[len(series)-1]
		if err := write(journal.New(journal.KindBars, "", map[string]any{"symbol": s, "bars": len(series), "last": last.Date.Format("2006-01-02"), "source": last.Source})); err != nil {
			return out, err
		}
	}

	// 3. Quotes, then today's bar in memory if the market has moved on from
	// the file: the strategy sees the close it is about to trade at.
	names := sortedKeys(symbols)
	quotes, err := r.Quotes.Quotes(ctx, names)
	if err != nil {
		return fail("quotes: " + err.Error())
	}
	bySymbol := map[string]marketdata.Quote{}
	for _, q := range quotes {
		bySymbol[q.Symbol] = q
		if err := write(journal.New(journal.KindQuote, "", q)); err != nil {
			return out, err
		}
	}
	for _, s := range names {
		if _, ok := bySymbol[s]; !ok {
			return fail("quotes: none for " + s)
		}
	}
	day := now().UTC().Truncate(24 * time.Hour)
	for s, series := range hist {
		q := bySymbol[s]
		if last := series[len(series)-1]; last.Date.Before(day) && q.Last > 0 {
			hist[s] = append(series, bars.Bar{Date: day, Open: q.Last, High: q.Last, Low: q.Last, Close: q.Last, AdjClose: q.Last, Source: "quote", FetchedAt: q.At})
		}
	}

	// 4. The account.
	equity, err := r.Broker.Equity(ctx)
	if err != nil {
		return fail("broker: equity: " + err.Error())
	}
	out.Equity = equity
	positions, err := r.Broker.Positions(ctx)
	if err != nil {
		return fail("broker: positions: " + err.Error())
	}
	held := map[string]float64{}
	for _, p := range positions {
		held[p.Symbol] += p.Qty
	}

	// 5. Targets, summed across strategies.
	targets := map[string]float64{}
	limits := risk.Limits{}
	for i, a := range armed {
		sub := map[string][]bars.Bar{}
		for _, s := range a.spec.Universe {
			sub[s] = hist[s]
		}
		w, err := a.st.Targets(sub, positions)
		if err != nil {
			return fail(a.spec.Name + ": " + err.Error())
		}
		out.Strategies[indexOf(out.Strategies, a.spec.Name)].Targets = w
		for s, v := range w {
			targets[s] += v
		}
		if err := write(journal.New(journal.KindTargets, "", map[string]any{"strategy": a.spec.Name, "weights": w})); err != nil {
			return out, err
		}
		// Limits come from the specs: the tightest per-leg cap, the sum of
		// gross leverage across strategies (each backtested its own).
		if i == 0 || a.spec.Sizing.MaxNotionalPerLegUSD < limits.MaxNotionalPerLegUSD {
			limits.MaxNotionalPerLegUSD = a.spec.Sizing.MaxNotionalPerLegUSD
		}
		limits.GrossLeverage += a.spec.Sizing.GrossLeverage
	}

	// 6. Intents: the difference between what the targets want and what is
	// held, at the quote, as market orders.
	var intents []broker.Order
	minNotional := r.Cfg.MinNotionalUSD
	if minNotional <= 0 {
		minNotional = 5
	}
	n := 0
	for _, s := range names {
		q := bySymbol[s]
		price := q.Last
		if price <= 0 {
			price = (q.Bid + q.Ask) / 2
		}
		if price <= 0 {
			return fail("quotes: no usable price for " + s)
		}
		want := targets[s] * equity / price
		delta := want - held[s]
		if math.Abs(delta*price) < minNotional {
			continue
		}
		n++
		o := broker.Order{IntentID: fmt.Sprintf("%s-%02d-%s", runID, n, s), Symbol: s, Qty: math.Abs(delta), Side: broker.Buy}
		if delta < 0 {
			o.Side = broker.Sell
		}
		intents = append(intents, o)
	}

	// 7. The gate, and the kill switch even when there is nothing to send.
	state, err := r.state(ctx, now(), equity, positions, quotes)
	if err != nil {
		return fail("accounting: " + err.Error())
	}
	gate := risk.NewGate(r.Cfg.Risk, limits)
	if reason, kill := gate.Kill(state); kill {
		if err := halt.Halt(r.Cfg.DataDir, halt.Marker{Reason: reason, By: "risk gate", At: now()}); err != nil {
			return fail("halt: " + err.Error())
		}
		out.Status, out.Reason = "halted", reason
		if err := write(journal.New(journal.KindHalt, "", map[string]string{"reason": reason, "by": "risk gate"})); err != nil {
			return out, err
		}
		log.Warn("kill switch", "run_id", runID, "reason", reason)
		return out, write(journal.New(journal.KindRun, "", map[string]string{"status": "halted", "reason": reason}))
	}
	decisions := gate.Check(ctx, intents, state)
	byIntent := map[string]broker.Order{}
	for _, o := range intents {
		byIntent[o.IntentID] = o
	}
	for _, d := range decisions {
		if err := write(journal.Decision(d.IntentID, d.Allowed, map[string]any{"reason": d.Reason, "qty": d.Qty, "order": byIntent[d.IntentID], "needs_confirm": d.NeedsConfirm})); err != nil {
			return out, err
		}
		if !d.Allowed {
			out.Rejected++
		}
	}

	// 8. Orders, each journalled the instant before it goes out; fills after.
	runStart := now()
	for _, d := range decisions {
		if !d.Allowed {
			continue
		}
		o := byIntent[d.IntentID]
		o.Qty = d.Qty
		if err := write(journal.New(journal.KindOrderSubmitted, o.IntentID, map[string]any{"order": o})); err != nil {
			return out, err
		}
		id, err := r.Broker.Place(ctx, o)
		if err != nil {
			if err := write(journal.New(journal.KindError, o.IntentID, map[string]string{"error": "place: " + err.Error()})); err != nil {
				return out, err
			}
			continue
		}
		out.Orders++
		log.Info("order placed", "run_id", runID, "intent", o.IntentID, "order_id", id, "side", o.Side.String(), "symbol", o.Symbol, "qty", o.Qty)
	}
	fills, err := r.Broker.Fills(ctx, runStart)
	if err != nil {
		return fail("broker: fills: " + err.Error())
	}
	for _, f := range fills {
		if err := write(journal.New(journal.KindFill, f.IntentID, f)); err != nil {
			return out, err
		}
	}
	if eq, err := r.Broker.Equity(ctx); err == nil {
		out.Equity = eq
	}
	out.Status = "completed"
	log.Info("run completed", "run_id", runID, "orders", out.Orders, "rejected", out.Rejected, "equity", out.Equity)
	return out, write(journal.New(journal.KindRun, "", map[string]any{"status": "completed", "orders": out.Orders, "rejected": out.Rejected, "equity": out.Equity}))
}

type armedSpec struct {
	spec *spec.Spec
	st   strategy.Strategy
}

// state gathers what the gate needs from the account.
func (r *Runner) state(ctx context.Context, now time.Time, equity float64, positions []strategy.Position, quotes []marketdata.Quote) (risk.State, error) {
	s := risk.State{Now: now, Equity: equity, Positions: positions, Quotes: quotes, Live: r.Cfg.Mode == "live"}
	var err error
	if s.StartOfDayEquity, err = r.Books.StartOfDayEquity(ctx, now); err != nil {
		return s, err
	}
	if s.HighWaterEquity, err = r.Books.HighWaterEquity(ctx); err != nil {
		return s, err
	}
	if s.DailyLimitHits, err = r.Books.DailyLimitHits(ctx, now, r.Cfg.Risk.DailyLossLimit); err != nil {
		return s, err
	}
	return s, nil
}

// Done reports whether runID already ran to a conclusion: the scheduler's
// idempotence check. A journal that ends in "started" was cut off and the
// day is run again.
func Done(dataDir, runID string) bool {
	s, err := journal.Summarize(journal.File(JournalDir(dataDir), runID))
	if err != nil {
		return false
	}
	return s.Status == "completed" || s.Status == "halted" || s.Status == "failed"
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func indexOf(list []StrategyOutcome, name string) int {
	for i, s := range list {
		if s.Name == name {
			return i
		}
	}
	return len(list) - 1
}

// specName is what to call a spec in the journal: its declared name, or the
// file's when it did not parse far enough to have one.
func specName(so StrategyOutcome) string {
	if so.Name != "" {
		return so.Name
	}
	base := filepath.Base(so.Path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// noArmedReason says why the run has nothing to trade, in one line a phone
// can read: an empty specs directory is a different problem from three specs
// the runtime refused, and the fix is different too.
func noArmedReason(specsDir string, outcomes []StrategyOutcome) string {
	if len(outcomes) == 0 {
		return "no strategy specs in " + specsDir + ": nothing to trade"
	}
	reasons := make([]string, 0, len(outcomes))
	for _, so := range outcomes {
		reasons = append(reasons, specName(so)+": "+so.Reason)
	}
	return fmt.Sprintf("no armed strategy: %s refused: %s",
		plural(len(outcomes), "spec"), strings.Join(reasons, "; "))
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
