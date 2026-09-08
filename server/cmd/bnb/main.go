// Command bnb runs Bulls and Bears: the REST API, the PWA, the scheduler, the
// paper broker, the risk gate and the journal, from a single binary.
//
//	bnb                     serve, and run the daily cycle before each close
//	bnb run [-run-id DATE]  run one cycle now and exit
//	bnb backtest -spec FILE replay a spec over the bars on disk and print the numbers
//	bnb halt [reason]       write the halt marker; bnb resume removes it; bnb status reads it
//	bnb login               sign in to the Robinhood MCP from a desktop browser; writes the token file
//	bnb discover            initialize against the MCP and print tools/list (Phase 0)
//	bnb version             print the build
//
// Trading is stopped by a marker file in the data directory (see
// internal/halt). Halting and resuming work whether or not the server is up,
// which is what a kill switch has to do.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	iofs "io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/api"
	"github.com/chinmay28/bulls-and-bears/server/internal/backtest"
	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/broker/paper"
	"github.com/chinmay28/bulls-and-bears/server/internal/halt"
	"github.com/chinmay28/bulls-and-bears/server/internal/marketdata/replay"
	"github.com/chinmay28/bulls-and-bears/server/internal/marketdata/yahoo"
	"github.com/chinmay28/bulls-and-bears/server/internal/mcp"
	"github.com/chinmay28/bulls-and-bears/server/internal/mcp/oauth"
	"github.com/chinmay28/bulls-and-bears/server/internal/research"
	"github.com/chinmay28/bulls-and-bears/server/internal/risk"
	"github.com/chinmay28/bulls-and-bears/server/internal/runner"
	"github.com/chinmay28/bulls-and-bears/server/internal/sched"
	"github.com/chinmay28/bulls-and-bears/server/internal/spec"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
	_ "github.com/chinmay28/bulls-and-bears/server/internal/strategy/donchian"
	_ "github.com/chinmay28/bulls-and-bears/server/internal/strategy/momentum"
	_ "github.com/chinmay28/bulls-and-bears/server/internal/strategy/pairs"
	_ "github.com/chinmay28/bulls-and-bears/server/internal/strategy/ratio"
	_ "github.com/chinmay28/bulls-and-bears/server/internal/strategy/riskparity"
	_ "github.com/chinmay28/bulls-and-bears/server/internal/strategy/trend"
	_ "github.com/chinmay28/bulls-and-bears/server/internal/strategy/tsmomentum"
	"github.com/chinmay28/bulls-and-bears/server/internal/version"
	"github.com/chinmay28/bulls-and-bears/server/internal/web"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// tradingFlags are shared by the server and `bnb run`: where everything is.
type tradingFlags struct {
	dataDir, specsDir, barsDir, riskPath string
	researchDir                          string
	startingCash                         float64
	spreadBps                            float64
	allowMixed                           bool
	offset                               time.Duration
}

func addTradingFlags(fs *flag.FlagSet) *tradingFlags {
	t := &tradingFlags{}
	fs.StringVar(&t.dataDir, "data", envOr("BNB_DATA", "data"), "directory for the halt marker, journal and paper book")
	fs.StringVar(&t.specsDir, "specs", envOr("BNB_SPECS", "specs"), "directory of strategy specs (*.yaml)")
	fs.StringVar(&t.barsDir, "bars", envOr("BNB_BARS", ""), "directory of daily bars (<SYMBOL>.parquet); default <data>/bars")
	fs.StringVar(&t.riskPath, "risk", envOr("BNB_RISK", ""), "risk.yaml with the gate's thresholds; default: the plan's defaults")
	fs.StringVar(&t.researchDir, "research", envOr("BNB_RESEARCH", ""), "the research/ tree the app can run studies from; default: beside the specs directory")
	fs.Float64Var(&t.startingCash, "starting-cash", 10_000, "cash the paper book opens with, the first time only")
	fs.Float64Var(&t.spreadBps, "replay-spread-bps", 4, "bid-ask spread the replay quote source puts around the last close")
	fs.BoolVar(&t.allowMixed, "allow-mixed-sources", false, "accept a symbol whose bars mix sources")
	fs.DurationVar(&t.offset, "run-offset", 10*time.Minute, "how long before the close the daily run fires")
	return t
}

func (t *tradingFlags) finish() error {
	if t.barsDir == "" {
		t.barsDir = filepath.Join(t.dataDir, "bars")
	}
	if t.researchDir == "" {
		// The checkout keeps specs/ and research/ side by side, and the
		// installer points -specs into the checkout, so this finds the tree
		// on an installed machine and in a development checkout alike.
		t.researchDir = filepath.Join(filepath.Dir(filepath.Clean(t.specsDir)), "research")
	}
	// The data directory holds a broker token one day; it is private from the
	// start rather than tightened later.
	return os.MkdirAll(t.dataDir, 0o700)
}

func (t *tradingFlags) riskConfig() (risk.Config, error) {
	if t.riskPath == "" {
		return risk.DefaultConfig(), nil
	}
	return risk.LoadConfig(t.riskPath)
}

// trading opens the paper book and builds the runner. quotes come from the
// bars on disk until a live feed exists.
func (t *tradingFlags) trading(log *slog.Logger) (*paper.Book, *runner.Runner, risk.Config, error) {
	cfg, err := t.riskConfig()
	if err != nil {
		return nil, nil, cfg, err
	}
	quotes := &replay.Source{Dir: t.barsDir, SpreadBps: t.spreadBps}
	book, err := paper.Open(filepath.Join(t.dataDir, "bnb.db"), quotes, paper.Options{StartingCash: t.startingCash, Costs: paper.Costs{SlippageBps: 5}})
	if err != nil {
		return nil, nil, cfg, err
	}
	r := &runner.Runner{
		Cfg:    runner.Config{DataDir: t.dataDir, SpecsDir: t.specsDir, BarsDir: t.barsDir, Mode: "dry-run", Risk: cfg, AllowMixedSources: t.allowMixed},
		Quotes: quotes, Broker: book, Books: book, Log: log,
	}
	return book, r, cfg, nil
}

func run(args []string) error {
	// Subcommands first; anything else — flags or nothing — serves.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return runCommand(args[0], args[1:])
	}

	fs := flag.NewFlagSet("bnb", flag.ContinueOnError)
	var (
		addr    = fs.String("addr", envOr("BNB_ADDR", ":8877"), "listen address (host:port)")
		pin     = fs.String("pin", os.Getenv("BNB_PIN"), "optional PIN required to use the UI; empty disables authentication")
		ref     = fs.String("self-ref", envOr("BNB_REF", "main"), "git ref a self-update builds from by default")
		verbose = fs.Bool("v", false, "verbose logging")
	)
	t := addTradingFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := t.finish(); err != nil {
		return err
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	book, r, riskCfg, err := t.trading(log)
	if err != nil {
		return err
	}
	defer book.Close()

	researcher := &research.Runner{
		Dir: t.researchDir, DataDir: t.dataDir, SpecsDir: t.specsDir, BarsDir: t.barsDir,
		Installer: &research.Installer{Client: &http.Client{Timeout: 10 * time.Minute}}, Log: log,
	}
	// Once a month, outside market hours, every installed spec is re-run
	// with its training cutoff fixed; a spec that no longer earns its place
	// is left to expire at its TTL.
	schedule := &research.Schedule{Runner: researcher, SpecsDir: t.specsDir, DataDir: t.dataDir, Log: log}

	auth := api.NewPinAuth(*pin)
	apiSrv := &api.Server{
		Log: log, Version: appVersion(), DataDir: t.dataDir, SpecsDir: t.specsDir, BarsDir: t.barsDir,
		Book: book, Risk: riskCfg, RunOffset: t.offset, Ref: *ref, Auth: auth,
		RunNow: func(ctx context.Context, runID string) (runner.Outcome, error) { return r.Run(ctx, runID) },
		// Bars come from Yahoo, as docs/DISCOVERY.md decided until the MCP is
		// known to serve history. The fetch is on demand only: nothing here
		// reaches the network unless the operator asks it to.
		Fetcher: &yahoo.Client{HTTP: &http.Client{Timeout: 30 * time.Second}},
		// The studies run from the phone with their output pointed at the
		// directories above; uv is downloaded if the machine has none.
		Research: researcher,
		Schedule: schedule,
	}

	loop := &sched.Loop{
		Offset: t.offset,
		Done:   func(id string) bool { return runner.Done(t.dataDir, id) },
		Halted: func() (string, bool) {
			m, err := halt.Status(t.dataDir)
			if errors.Is(err, halt.ErrNotHalted) {
				return "", false
			}
			return m.Reason, true
		},
		Run: func(ctx context.Context, s sched.Session) error {
			_, err := r.Run(ctx, s.RunID())
			return err
		},
		Log: log,
	}
	go loop.Serve(ctx)
	go schedule.Serve(ctx)

	mux := http.NewServeMux()
	mux.Handle("/api/", apiSrv.Routes())
	mux.Handle("/", web.Handler())

	srv := &http.Server{
		Addr:              *addr,
		Handler:           auth.Middleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("bnb listening", "version", apiSrv.Version, "addr", *addr, "data", t.dataDir, "specs", t.specsDir, "bars", t.barsDir, "research", t.researchDir,
			"auth", authMode(auth), "halted", halt.Halted(t.dataDir))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func runCommand(name string, args []string) error {
	fs := flag.NewFlagSet("bnb "+name, flag.ContinueOnError)
	switch name {
	case "version":
		fmt.Println(appVersion())
		return nil
	case "halt", "resume", "status":
		dataDir := fs.String("data", envOr("BNB_DATA", "data"), "data directory")
		if err := fs.Parse(args); err != nil {
			return err
		}
		return haltCommand(name, *dataDir, fs.Args())
	case "run":
		t := addTradingFlags(fs)
		runID := fs.String("run-id", "", "the trading date to run as (default: the next session)")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if err := t.finish(); err != nil {
			return err
		}
		log := slog.New(slog.NewTextHandler(os.Stderr, nil))
		book, r, _, err := t.trading(log)
		if err != nil {
			return err
		}
		defer book.Close()
		if *runID == "" {
			s, err := sched.NextClose(time.Now())
			if err != nil {
				return err
			}
			*runID = s.RunID()
		}
		out, err := r.Run(context.Background(), *runID)
		if err != nil {
			return err
		}
		fmt.Printf("run %s: %s", out.RunID, out.Status)
		if out.Reason != "" {
			fmt.Printf(" (%s)", out.Reason)
		}
		fmt.Printf("\n  orders %d, rejected %d, equity %.2f\n", out.Orders, out.Rejected, out.Equity)
		for _, s := range out.Strategies {
			if s.Armed {
				fmt.Printf("  %s: armed, targets %v\n", s.Name, s.Targets)
			} else {
				fmt.Printf("  %s: %s\n", filepath.Base(s.Path), s.Reason)
			}
		}
		fmt.Printf("  journal %s\n", filepath.Join(runner.JournalDir(t.dataDir), out.RunID+".jsonl"))
		if out.Status != "completed" {
			os.Exit(2)
		}
		return nil
	case "backtest":
		return backtestCommand(fs, args)
	case "login", "discover":
		return mcpCommand(name, fs, args)
	}
	return fmt.Errorf("unknown command %q (want run, backtest, login, discover, halt, resume, status or version)", name)
}

func haltCommand(name, dataDir string, rest []string) error {
	switch name {
	case "halt":
		reason := strings.TrimSpace(strings.Join(rest, " "))
		if reason == "" {
			reason = "halted from the command line"
		}
		if err := halt.Halt(dataDir, halt.Marker{Reason: reason, By: "cli"}); err != nil {
			return err
		}
		m, _ := halt.Status(dataDir)
		fmt.Printf("halted: %s (%s, %s)\n", m.Reason, m.By, m.At.Format(time.RFC3339))
	case "resume":
		if err := halt.Resume(dataDir); err != nil {
			return err
		}
		fmt.Println("resumed")
	case "status":
		m, err := halt.Status(dataDir)
		if errors.Is(err, halt.ErrNotHalted) {
			fmt.Println("not halted")
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Printf("halted: %s (%s, %s)\n", m.Reason, m.By, m.At.Format(time.RFC3339))
	}
	return nil
}

// backtestCommand replays a spec over the bars on disk and prints the numbers
// the research side printed, so the two can be compared by eye and by `make
// backtest-compare`.
func backtestCommand(fs *flag.FlagSet, args []string) error {
	var (
		specPath = fs.String("spec", "", "strategy spec to replay (required)")
		barsDir  = fs.String("bars", envOr("BNB_BARS", "data/bars"), "directory of daily bars")
		from     = fs.String("from", "", "first day to trade, YYYY-MM-DD (default: the spec's test window)")
		to       = fs.String("to", "", "last day, YYYY-MM-DD (default: the last bar)")
		csvOut   = fs.String("csv", "", "write the equity curve here as date,equity")
		ignore   = fs.Bool("ignore-refusal", false, "replay a spec the runtime would refuse (stale, low Sharpe)")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *specPath == "" {
		return errors.New("bnb backtest: -spec is required")
	}
	sp, err := spec.Load(*specPath, time.Now())
	var refused *spec.Refused
	if errors.As(err, &refused) && *ignore {
		// The refusal is the runtime's policy, not a defect in the spec:
		// the spec it carries is whole and replayable.
		fmt.Fprintln(os.Stderr, "note:", refused.Reason)
		sp, err = refused.Spec, nil
	}
	if err != nil {
		return err
	}
	st, err := strategy.New(sp.Strategy, sp.Universe, sp.Params, sp.Sizing)
	if err != nil {
		return err
	}
	hist := map[string][]bars.Bar{}
	for _, sym := range sp.Universe {
		series, err := bars.Read(filepath.Join(*barsDir, sym+".parquet"))
		if err != nil {
			return err
		}
		if *to != "" {
			end, err := time.Parse("2006-01-02", *to)
			if err != nil {
				return err
			}
			series = bars.Window(series, time.Time{}, end)
		}
		hist[sym] = series
	}
	start := sp.Provenance.TestWindow.From
	if *from != "" {
		if start, err = time.Parse("2006-01-02", *from); err != nil {
			return err
		}
	}
	fill, err := backtest.ParseFill(sp.Execution.FillAt)
	if err != nil {
		return err
	}
	res, err := backtest.StrategyFromFill(st, hist, start, backtest.Costs{CommissionUSD: sp.Provenance.CostModel.CommissionUSD, SlippageBps: sp.Provenance.CostModel.SlippageBps}, fill)
	if err != nil {
		return err
	}
	n := len(res.Equity)
	fmt.Printf("%s: %s..%s, %d bars, %d trades\n", sp.Name, res.Equity[0].Date.Format("2006-01-02"), res.Equity[n-1].Date.Format("2006-01-02"), n, len(res.Trades))
	if res.SummaryErr != nil {
		fmt.Printf("  sharpe: undefined (%v)\n", res.SummaryErr)
	} else {
		fmt.Printf("  sharpe %.3f (spec says %.3f out of sample)\n", res.Summary.Sharpe, sp.Provenance.OOSSharpe)
	}
	fmt.Printf("  max drawdown %.3f%%, %d bars under water, total return %.2f%%\n", res.Summary.MaxDrawdown*100, res.Summary.MaxDrawdownDuration, res.Summary.TotalReturn*100)
	if *csvOut != "" {
		f, err := os.Create(*csvOut)
		if err != nil {
			return err
		}
		fmt.Fprintln(f, "date,equity")
		for _, p := range res.Equity {
			fmt.Fprintf(f, "%s,%.17g\n", p.Date.Format("2006-01-02"), p.Equity)
		}
		f.Close()
		fmt.Printf("  equity curve written to %s\n", *csvOut)
	}
	return nil
}

// DefaultMCP is the Robinhood Trading MCP endpoint from docs/PLAN.md §2.
const DefaultMCP = "https://agent.robinhood.com/mcp/trading"

// mcpCommand is Phase 0 in two commands. `bnb login` runs the OAuth flow on
// a machine with a browser and writes the token file; `bnb discover`
// initializes against the server with that token, prints tools/list as JSON
// and saves it under docs/discovery/ when asked to. The token file is what
// gets copied to the machine that trades.
func mcpCommand(name string, fs *flag.FlagSet, args []string) error {
	var (
		endpoint = fs.String("mcp", envOr("BNB_MCP", DefaultMCP), "MCP endpoint URL")
		dataDir  = fs.String("data", envOr("BNB_DATA", "data"), "data directory (holds the token file)")
		clientID = fs.String("client-id", "", "OAuth client id, when the server offers no dynamic registration")
		scopes   = fs.String("scopes", "", "space-separated OAuth scopes to request")
		out      = fs.String("out", "", "discover: write tools/list JSON here as well as printing it")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return err
	}
	store := oauth.Store{Path: filepath.Join(*dataDir, "robinhood-token.json")}
	hc := &http.Client{Timeout: 60 * time.Second}
	ctx := context.Background()

	if name == "login" {
		// Ask for the challenge first: the server says where to authorize.
		probe := &mcp.Client{Endpoint: *endpoint, HTTP: hc}
		_, err := probe.Initialize(ctx)
		var ae *mcp.AuthError
		challenge := ""
		if errors.As(err, &ae) {
			challenge = ae.Challenge
		} else if err != nil {
			return fmt.Errorf("probing %s: %w", *endpoint, err)
		} else {
			fmt.Println("the server accepted an unauthenticated initialize; no login needed")
			return nil
		}
		meta, err := oauth.Discover(ctx, hc, *endpoint, challenge)
		if err != nil {
			return err
		}
		id := *clientID
		if id == "" {
			if id, err = oauth.Register(ctx, hc, meta, "http://127.0.0.1/callback", "Bulls and Bears"); err != nil {
				return err
			}
		}
		var sc []string
		if *scopes != "" {
			sc = strings.Fields(*scopes)
		}
		tok, err := oauth.Login(ctx, hc, meta, id, sc, func(u string) {
			fmt.Println("Open this in a desktop browser and sign in:")
			fmt.Println()
			fmt.Println("  " + u)
			fmt.Println()
			fmt.Println("Waiting for the redirect back to localhost…")
		})
		if err != nil {
			return err
		}
		if err := store.Save(tok); err != nil {
			return err
		}
		fmt.Printf("signed in; token written to %s (0600). Copy it to the trading machine's data directory.\n", store.Path)
		return nil
	}

	c := &mcp.Client{Endpoint: *endpoint, HTTP: hc, Token: &oauth.Source{Store: store, HTTP: hc}, Name: "bnb", Version: appVersion()}
	info, err := c.Initialize(ctx)
	if err != nil {
		var ae *mcp.AuthError
		if errors.As(err, &ae) || errors.Is(err, iofs.ErrNotExist) {
			return fmt.Errorf("not signed in (%v): run `bnb login` on a machine with a browser and copy %s here", err, store.Path)
		}
		return err
	}
	tools, err := c.ListTools(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "server %s %s, protocol %s, session %q, %d tools\n", info.ServerInfo.Name, info.ServerInfo.Version, info.ProtocolVersion, c.SessionID(), len(tools))
	b, _ := json.MarshalIndent(map[string]any{"server": info, "tools": tools}, "", "  ")
	fmt.Println(string(b))
	if *out != "" {
		if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "written to", *out)
	}
	return nil
}

func authMode(a *api.PinAuth) string {
	if a == nil {
		return "none"
	}
	return "pin"
}

// appVersion reports the build this binary came from: vYEAR.MONTH.PATCH, where
// the patch number is the repository's commit count that `make build` and the
// installer stamp in (see internal/version).
//
// A build made without that stamp has no commit count to report, so it says so
// — patch 0 — and pins down what it actually is with the revision Go records,
// which is the more useful half of the answer for a build off someone's branch.
func appVersion() string {
	if version.Stamped() {
		return version.String()
	}
	if rev := revision(); rev != "" {
		return version.String() + "+" + rev
	}
	return version.String()
}

// revision is the short commit Go stamped into the binary, empty if it didn't
// (no VCS at build time, or -buildvcs=false).
func revision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			if len(setting.Value) > 12 {
				return setting.Value[:12]
			}
			return setting.Value
		}
	}
	return ""
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
