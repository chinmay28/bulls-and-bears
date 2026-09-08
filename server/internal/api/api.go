// Package api exposes Bulls and Bears' REST API over HTTP.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/bars/refill"
	"github.com/chinmay28/bulls-and-bears/server/internal/broker/paper"
	"github.com/chinmay28/bulls-and-bears/server/internal/research"
	"github.com/chinmay28/bulls-and-bears/server/internal/risk"
	"github.com/chinmay28/bulls-and-bears/server/internal/runner"
)

// Server holds the dependencies the handlers need.
type Server struct {
	Log *slog.Logger
	// Version is the build this binary was made from.
	Version string
	// DataDir holds the halt marker, the journal and the paper book.
	DataDir string
	// SpecsDir and BarsDir are where the strategies and their history live;
	// empty when the server runs without a runner.
	SpecsDir, BarsDir string
	// Book is the paper account; nil when there is none.
	Book *paper.Book
	// Risk is the thresholds in force, for the meters.
	Risk risk.Config
	// Fetcher is where a bars refill gets its history; nil when the server
	// runs without one, and the refresh endpoint then says so.
	Fetcher refill.Fetcher
	// Research runs the Python studies from the app; nil when the server
	// has no research tree, and the Research tab then says so.
	Research *research.Runner
	// RunNow performs a cycle for a run id; nil when there is no runner.
	RunNow func(ctx context.Context, runID string) (runner.Outcome, error)
	// RunOffset is how long before the close the scheduler fires.
	RunOffset time.Duration
	// Ref is the git ref a self-update builds from by default.
	Ref string
	// Auth is nil when Bulls and Bears runs without a PIN.
	Auth *PinAuth
}

// Routes returns the API mux, to be mounted under /api/.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)

	mux.HandleFunc("GET /api/session", s.handleSessionStatus)
	mux.HandleFunc("POST /api/session", s.handleLogin)

	mux.HandleFunc("GET /api/self", s.handleGetSelf)
	mux.HandleFunc("GET /api/overview", s.handleOverview)

	mux.HandleFunc("GET /api/halt", s.handleGetHalt)
	mux.HandleFunc("POST /api/halt", s.handleHalt)
	mux.HandleFunc("DELETE /api/halt", s.handleResume)

	mux.HandleFunc("GET /api/strategies", s.handleListStrategies)
	mux.HandleFunc("POST /api/strategies", s.handleImportStrategy)
	mux.HandleFunc("GET /api/strategies/{name}", s.handleGetStrategy)
	mux.HandleFunc("DELETE /api/strategies/{name}", s.handleDeleteStrategy)
	mux.HandleFunc("POST /api/strategies/{name}/backtest", s.handleBacktest)
	mux.HandleFunc("GET /api/bars", s.handleBars)
	mux.HandleFunc("POST /api/bars/refresh", s.handleRefreshBars)

	mux.HandleFunc("GET /api/book", s.handleBook)

	mux.HandleFunc("GET /api/research", s.handleResearch)
	mux.HandleFunc("POST /api/research/setup", s.handleResearchSetup)
	mux.HandleFunc("POST /api/research/runs", s.handleResearchRun)
	mux.HandleFunc("GET /api/research/jobs/{id}", s.handleResearchJob)
	mux.HandleFunc("DELETE /api/research/jobs/{id}", s.handleResearchCancel)

	mux.HandleFunc("GET /api/runs", s.handleListRuns)
	mux.HandleFunc("GET /api/runs/{id}", s.handleGetRun)
	mux.HandleFunc("GET /api/runs/{id}/jsonl", s.handleRunJSONL)
	mux.HandleFunc("POST /api/run", s.handleRunNow)

	return mux
}

// handleHealth is what the installer's health check polls, so it stays cheap
// and dependency-free. The version tells an upgrade whether the binary that
// came back up is the new one.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.Version})
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already sent; nothing useful left to do.
		return
	}
}

type apiError struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, apiError{Error: msg})
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.New("invalid request body: " + err.Error())
	}
	return nil
}
