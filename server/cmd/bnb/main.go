// Command bnb runs Bulls and Bears: the REST API, the PWA and — as the phases
// in docs/PLAN.md land — the scheduler, paper broker and risk gate, from a
// single binary.
//
// Trading is stopped by a marker file in the data directory (see
// internal/halt): `bnb halt` writes it, `bnb resume` removes it, and the
// running server honours it. Both work whether or not the server is up, which
// is what a kill switch has to do.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/api"
	"github.com/chinmay28/bulls-and-bears/server/internal/halt"
	"github.com/chinmay28/bulls-and-bears/server/internal/version"
	"github.com/chinmay28/bulls-and-bears/server/internal/web"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// Subcommands first: `bnb halt`, `bnb resume`, `bnb version`. Anything else
	// — flags or nothing — starts the server.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return runCommand(args[0], args[1:])
	}

	fs := flag.NewFlagSet("bnb", flag.ContinueOnError)
	var (
		addr    = fs.String("addr", envOr("BNB_ADDR", ":8877"), "listen address (host:port)")
		dataDir = fs.String("data", envOr("BNB_DATA", "data"), "directory for the halt marker, journal and paper book")
		pin     = fs.String("pin", os.Getenv("BNB_PIN"), "optional PIN required to use the UI; empty disables authentication")
		ref     = fs.String("self-ref", envOr("BNB_REF", "main"), "git ref a self-update builds from by default")
		verbose = fs.Bool("v", false, "verbose logging")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	// The data directory holds a broker token one day; it is private from the
	// start rather than tightened later.
	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	auth := api.NewPinAuth(*pin)
	apiSrv := &api.Server{
		Log: log, Version: appVersion(), DataDir: *dataDir, Ref: *ref, Auth: auth,
	}

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
		log.Info("bnb listening", "version", apiSrv.Version, "addr", *addr, "data", *dataDir,
			"auth", authMode(auth), "halted", halt.Halted(*dataDir))
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
	dataDir := fs.String("data", envOr("BNB_DATA", "data"), "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch name {
	case "version":
		fmt.Println(appVersion())
		return nil
	case "halt":
		reason := strings.TrimSpace(strings.Join(fs.Args(), " "))
		if reason == "" {
			reason = "halted from the command line"
		}
		if err := halt.Halt(*dataDir, halt.Marker{Reason: reason, By: "cli"}); err != nil {
			return err
		}
		m, _ := halt.Status(*dataDir)
		fmt.Printf("halted: %s (%s, %s)\n", m.Reason, m.By, m.At.Format(time.RFC3339))
		return nil
	case "resume":
		if err := halt.Resume(*dataDir); err != nil {
			return err
		}
		fmt.Println("resumed")
		return nil
	case "status":
		m, err := halt.Status(*dataDir)
		if errors.Is(err, halt.ErrNotHalted) {
			fmt.Println("not halted")
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Printf("halted: %s (%s, %s)\n", m.Reason, m.By, m.At.Format(time.RFC3339))
		return nil
	}
	return fmt.Errorf("unknown command %q (want halt, resume, status or version)", name)
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
