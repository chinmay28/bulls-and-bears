package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	iofs "io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/halt"
	"github.com/chinmay28/bulls-and-bears/server/internal/mcp"
	"github.com/chinmay28/bulls-and-bears/server/internal/mcp/oauth"
	"github.com/chinmay28/bulls-and-bears/server/internal/risk"
	"github.com/chinmay28/bulls-and-bears/server/internal/robinhood"
	"github.com/chinmay28/bulls-and-bears/server/internal/rotation"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/xlksata"

	"github.com/google/uuid"
)

// rotateCommand runs the XLK/SATA rotation against the Robinhood account:
// `bnb rotate` serves the intraday loop, `bnb rotate -once` runs a single
// cycle now and prints what it decided.
//
// It is a separate command from `bnb run` because it is a separate runtime.
// `run` replays daily bars through the weights interface into the paper
// broker; this places single orders in one live account off intraday quotes
// (docs/DISCOVERY.md, "A strategy the weights interface cannot express").
func rotateCommand(fs *flag.FlagSet, args []string) error {
	var (
		endpoint = fs.String("mcp", envOr("BNB_MCP", DefaultMCP), "MCP endpoint URL")
		dataDir  = fs.String("data", envOr("BNB_DATA", "data"), "data directory (token, halt marker and the lot's state)")
		account  = fs.String("account", envOr("BNB_ACCOUNT", ""), "the Robinhood account number to trade; required")
		live     = fs.Bool("live", false, "place real orders; without it every order is reviewed and none is placed")
		once     = fs.Bool("once", false, "run one cycle now and exit")
		phase    = fs.String("phase", "", "with -once: entry, review or manage (default: whatever the clock says)")
		manage   = fs.Duration("manage-every", 15*time.Minute, "how often to run a management cycle between the two named phases")
		tick     = fs.Duration("tick", time.Minute, "how often to consult the clock")
		riskPath = fs.String("risk", envOr("BNB_RISK", ""), "risk.yaml with the gate's thresholds; default: the plan's defaults")
		maxOrder = fs.Float64("max-order-usd", 0, "refuse any single order above this notional; 0 is uncapped")
		yes      = fs.Bool("yes", false, "place live orders over the confirmation threshold without asking; a 100-share lot is always over it")
		history  = fs.Bool("history", false, "print every book the ledger holds and exit; touches nothing")
		cron     = fs.Bool("cron", false, "print a DST-safe routine schedule and exit; touches nothing")
		maxCyc   = fs.Int("max-cycles", 4, "with -once: how many cycles one invocation may run, so a two-step sequence finishes in one firing")
		settle   = fs.Duration("settle", 15*time.Second, "with -once: how long to wait between cycles for a fill to land")
		force    = fs.Bool("force", false, "with -once: run even though the exchange is shut")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *history {
		return printHistory(*dataDir)
	}
	if *cron {
		printCron()
		return nil
	}
	if *account == "" {
		return errors.New("rotate: -account is required; this places real orders and will not guess which account in")
	}
	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return err
	}

	// One rotation per data directory. Two would each read a flat book, each
	// decide to open the lot, and each open one.
	lock, err := rotation.Acquire(*dataDir)
	if err != nil {
		return err
	}
	defer lock.Release()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	engine, err := xlksata.New(xlksata.Defaults())
	if err != nil {
		return err
	}
	riskCfg := risk.DefaultConfig()
	if *riskPath != "" {
		if riskCfg, err = risk.LoadConfig(*riskPath); err != nil {
			return err
		}
	}
	times, err := rotation.DefaultTimes()
	if err != nil {
		return err
	}

	hc := &http.Client{Timeout: 60 * time.Second}
	store := oauth.Store{Path: filepath.Join(*dataDir, "robinhood-token.json")}
	mc := &mcp.Client{
		Endpoint: *endpoint, HTTP: hc,
		Token: &oauth.Source{Store: store, HTTP: hc},
		Name:  "bnb", Version: appVersion(),
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if _, err := mc.Initialize(ctx); err != nil {
		var ae *mcp.AuthError
		if errors.As(err, &ae) || errors.Is(err, iofs.ErrNotExist) {
			return fmt.Errorf("not signed in (%v): run `bnb login` on a machine with a browser and copy %s here", err, store.Path)
		}
		return err
	}
	rh := &robinhood.Client{Tools: mc, Account: *account}

	// The account assertion before anything else, so a wrong account or one
	// that cannot write the calls the rules need fails at startup rather
	// than at 07:12 with money on the line.
	acct, err := rh.CheckAccount(ctx, true)
	if err != nil {
		return err
	}
	mode := "review only (no orders will be placed)"
	if *live {
		mode = "LIVE — real orders, real money"
	}
	log.Info("rotate: account ready",
		"type", acct.Type, "option_level", acct.OptionLevel, "mode", mode)

	// A 100-share lot is roughly $18,800, far over the $500 confirmation
	// threshold, so an unattended live run refuses every entry without -yes.
	// Say so at startup rather than at 07:12.
	if *live && !*yes && riskCfg.ConfirmAboveUSD > 0 {
		log.Warn("rotate: live without -yes",
			"threshold", riskCfg.ConfirmAboveUSD,
			"note", "orders over the threshold will be refused and journalled, not placed")
	}

	cyc := &rotation.Cycler{
		Broker: rh,
		Engine: engine,
		Exec: &rotation.Executor{
			Broker: rh,
			DryRun: !*live,
			// A fresh key per logical order. Reusing one is what makes a
			// retry safe; this code never retries, so a new key each time
			// is correct.
			RefID: func(xlksata.Intent) string { return uuid.NewString() },
		},
		Gate:       rotation.NewGate(riskCfg, *maxOrder),
		DataDir:    *dataDir,
		JournalDir: filepath.Join(*dataDir, "journal"),
		Confirmed:  *yes,
		Live:       *live,
		IntentID:   uuid.NewString,
	}
	// run reports whether the cycle moved anything, which is how a one-shot
	// invocation knows it has reached a steady state.
	run := func(ctx context.Context, p xlksata.Phase, now time.Time) (bool, error) {
		res, err := cyc.Run(ctx, p, now)
		report(log, res, err)
		return res.Settled != "" || len(res.Placed) > 0, err
	}

	if *once {
		p, ok := times.Phase(time.Now())
		if *phase != "" {
			if p, ok = parsePhase(*phase); !ok {
				return fmt.Errorf("rotate: %q is not a phase (want entry, review or manage)", *phase)
			}
		}
		if !ok && *phase == "" {
			return errors.New("rotate: the market is closed and no -phase was given")
		}
		// A named phase used to skip this. It must not: an order placed
		// into a shut market queues for the next open, where the quote it
		// was decided on is hours stale.
		if !times.InSession(time.Now()) && !*force {
			return errors.New("rotate: the exchange is shut; -force overrides")
		}
		return runOnce(ctx, run, p, *maxCyc, *settle, log)
	}

	loop := &rotation.Loop{
		Times: times, Tick: *tick, ManageEvery: *manage, Log: log,
		Run: func(ctx context.Context, p xlksata.Phase, now time.Time) error {
			_, err := run(ctx, p, now)
			return err
		},
		Halted: func() (string, bool) {
			m, err := halt.Status(*dataDir)
			if errors.Is(err, halt.ErrNotHalted) {
				return "", false
			}
			if err != nil {
				// An unreadable data directory counts as halted: the kill
				// switch fails closed or it is not a kill switch.
				return fmt.Sprintf("halt marker unreadable: %v", err), true
			}
			return m.Reason, true
		},
	}
	log.Info("rotate: serving", "entry", "07:12 PT", "review", "12:07 PT", "manage_every", *manage)
	loop.Serve(ctx)
	return nil
}

// printHistory dumps the ledger: every state the book has been in, with the
// time it was written. Read-only, and it does not need the broker, so it
// works on a copied data directory.
func printHistory(dataDir string) error {
	books, err := rotation.History(dataDir)
	if err != nil {
		return err
	}
	if len(books) == 0 {
		fmt.Printf("no ledger at %s\n", rotation.LedgerFile(dataDir))
		return nil
	}
	for _, b := range books {
		line := fmt.Sprintf("%s  %-8s", b.At.Format(time.RFC3339), b.State.Mode)
		if b.State.Open() {
			line += fmt.Sprintf(" entry %.4f  option %+.2f  div %+.2f  cost %.2f",
				b.State.EntryPrice, b.State.OptionPnL, b.State.Dividends, b.State.Costs)
		}
		if c := b.State.ShortCall; c != nil {
			line += fmt.Sprintf("  short %s %.2f @ %.2f", c.Expiration.Format("2006-01-02"), c.Strike, c.Credit)
		}
		if p := b.Pending; p != nil {
			line += fmt.Sprintf("  pending %s %s", p.Kind, p.OrderID)
		}
		fmt.Println(line)
	}
	fmt.Printf("\n%d entries in %s\n", len(books), rotation.LedgerFile(dataDir))
	return nil
}

// runOnce runs up to max cycles in one invocation, pausing between them for
// a fill to land, and stops as soon as a cycle has nothing left to do.
//
// This is what makes a scheduled firing as good as a running daemon. The
// strategy's sequences take more than one cycle on purpose — sell SATA, then
// buy XLK once the cash is there; buy the call back, then sell the shares —
// and a firing that ran a single cycle would leave the second half until the
// next one, an hour later. Running the sequence out here costs a few seconds
// and finishes the job.
func runOnce(ctx context.Context, run func(context.Context, xlksata.Phase, time.Time) (bool, error),
	phase xlksata.Phase, max int, settle time.Duration, log *slog.Logger) error {
	if max < 1 {
		max = 1
	}
	for i := 0; i < max; i++ {
		moved, err := run(ctx, phase, time.Now())
		if err != nil {
			return err
		}
		if !moved {
			log.Info("rotate: nothing left to do", "cycles", i+1)
			return nil
		}
		// After the first cycle the named phase is spent: what follows is
		// management, which is what carries a sequence to its end.
		phase = xlksata.PhaseManage
		if i == max-1 {
			break
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(settle):
		}
	}
	log.Info("rotate: cycle budget spent", "cycles", max,
		"note", "the sequence will continue on the next firing")
	return nil
}

// printCron prints a routine schedule that stays inside the trading session
// all year.
//
// The rules are stated in Pacific and cron is evaluated in UTC, which does
// not follow US daylight saving, so no fixed expression can hold 07:12 and
// 12:07 PT in both halves of the year. What can be held is the shape: a
// morning entry and a review four hours and fifty-five minutes later, both
// comfortably inside the session under either offset. 14:45 and 19:40 UTC
// are the times that do it — the window for keeping the full hold in session
// year-round is only 14:30 to 15:05, and this sits in the middle of it.
func printCron() {
	fmt.Print(`# Routine schedule for the XLK/SATA rotation. Cron is UTC.
#
# The rules say 07:12 and 12:07 Pacific. Cron cannot follow US daylight
# saving, so these hold the 4h55m gap between the two and stay inside the
# session under both offsets, rather than holding the wall-clock times:
#
#   14:45 UTC entry   = 07:45 PDT (open+75m) | 06:45 PST (open+15m)
#   19:40 UTC review  = 12:40 PDT (close-20m) | 11:40 PST (close-80m)
#
# Weekdays only; the exchange calendar is checked at run time, so a holiday
# firing exits without trading. On an early-close day the review falls after
# the close and is skipped: the lot carries to the next session.

45 14 * * 1-5   bnb rotate -account <ACCOUNT> -once -phase entry
40 19 * * 1-5   bnb rotate -account <ACCOUNT> -once -phase review
0  15-20 * * 1-5 bnb rotate -account <ACCOUNT> -once -phase manage

# Add -live -yes to place real orders. Without them every order is reviewed
# against the account and none is placed.
#
# Each firing runs up to -max-cycles cycles (default 4), so a sequence that
# needs two steps — sell SATA then buy XLK; buy the call back then sell the
# shares — finishes in one firing instead of waiting for the next.
`)
}

func parsePhase(s string) (xlksata.Phase, bool) {
	switch s {
	case "entry":
		return xlksata.PhaseEntry, true
	case "review":
		return xlksata.PhaseReview, true
	case "manage":
		return xlksata.PhaseManage, true
	}
	return 0, false
}

// report says what a cycle looked at and did, which for most cycles is
// nothing — and a cycle that did nothing still has to be able to say why.
func report(log *slog.Logger, res rotation.Result, err error) {
	attrs := []any{"phase", res.Phase.String(), "mode", res.After.Mode.String()}
	if res.Settled != "" {
		attrs = append(attrs, "settled", res.Settled)
	}
	if res.Plan.Reason != "" {
		attrs = append(attrs, "reason", res.Plan.Reason)
	}
	if res.After.Open() {
		attrs = append(attrs, "entry", fmt.Sprintf("%.4f", res.After.EntryPrice))
	}
	if err != nil {
		log.Error("rotate: cycle", append(attrs, "err", err)...)
		return
	}
	for _, d := range res.Decisions {
		if !d.Allowed {
			log.Warn("rotate: order refused", "intent", d.IntentID, "reason", d.Reason)
		} else if d.NeedsConfirm {
			log.Info("rotate: order over the confirmation threshold", "intent", d.IntentID, "reason", d.Reason)
		}
	}
	for _, p := range res.Placed {
		what := "reviewed"
		if p.Placed {
			what = "placed"
		}
		log.Info("rotate: order "+what,
			"kind", p.Intent.Kind.String(), "symbol", p.Intent.Symbol,
			"qty", p.Intent.Qty, "limit", p.Intent.Limit,
			"order_id", p.OrderID, "why", p.Intent.Why)
	}
	log.Info("rotate: cycle", attrs...)
}
