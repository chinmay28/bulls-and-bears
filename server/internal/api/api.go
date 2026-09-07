// Package api exposes Bulls and Bears' REST API over HTTP.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// Server holds the dependencies the handlers need.
type Server struct {
	Log *slog.Logger
	// Version is the build this binary was made from.
	Version string
	// DataDir holds the halt marker, the journal and the paper book.
	DataDir string
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
