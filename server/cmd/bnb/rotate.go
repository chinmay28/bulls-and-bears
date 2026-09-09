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
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *history {
		return printHistory(*dataDir)
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
	run := func(ctx context.Context, p xlksata.Phase, now time.Time) error {
		res, err := cyc.Run(ctx, p, now)
		report(log, res, err)
		return err
	}

	if *once {
		p, ok := times.Phase(time.Now())
		if *phase != "" {
			if p, ok = parsePhase(*phase); !ok {
				return fmt.Errorf("rotate: %q is not a phase (want entry, review or manage)", *phase)
			}
		}
		if !ok {
			return errors.New("rotate: the market is closed and no -phase was given")
		}
		return run(ctx, p, time.Now())
	}

	loop := &rotation.Loop{
		Times: times, Tick: *tick, ManageEvery: *manage, Run: run, Log: log,
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
