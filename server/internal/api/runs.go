package api

import (
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/journal"
	"github.com/chinmay28/bulls-and-bears/server/internal/runner"
	"github.com/chinmay28/bulls-and-bears/server/internal/sched"
)

// runView is one run as the list shows it.
type runView struct {
	RunID     string    `json:"runId"`
	Mode      string    `json:"mode"`
	Status    string    `json:"status"`
	Events    int       `json:"events"`
	Orders    int       `json:"orders"`
	Fills     int       `json:"fills"`
	Rejected  int       `json:"rejected"`
	Errors    int       `json:"errors"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	// Invariant is the journal checker's verdict: "" when every order
	// followed an allowed decision, else what it found.
	Invariant string `json:"invariant,omitempty"`
}

func (s *Server) runViews(limit int) ([]runView, error) {
	dir := runner.JournalDir(s.DataDir)
	ids, err := journal.ListRuns(dir)
	if err != nil {
		return nil, err
	}
	out := []runView{}
	for _, id := range ids {
		if limit > 0 && len(out) >= limit {
			break
		}
		v, err := s.runView(id)
		if err != nil {
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) runView(id string) (runView, error) {
	path := journal.File(runner.JournalDir(s.DataDir), id)
	sum, err := journal.Summarize(path)
	if err != nil {
		return runView{}, err
	}
	v := runView{RunID: id, Mode: sum.Mode, Status: sum.Status, Events: sum.Events, Orders: sum.Orders, Fills: sum.Fills,
		Rejected: sum.Rejected, Errors: sum.Errors, StartedAt: sum.StartedAt, EndedAt: sum.EndedAt}
	events, err := journal.Read(path)
	var trunc *journal.Truncated
	if err != nil && !errors.As(err, &trunc) {
		return v, nil
	}
	if err := journal.Check(events); err != nil {
		v.Invariant = err.Error()
	}
	return v, nil
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	views, err := s.runViews(60)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "runs: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	path := journal.File(runner.JournalDir(s.DataDir), id)
	events, err := journal.Read(path)
	var trunc *journal.Truncated
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "no run "+id)
		return
	}
	if err != nil && !errors.As(err, &trunc) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	v, _ := s.runView(id)
	writeJSON(w, http.StatusOK, map[string]any{"run": v, "events": events, "truncated": trunc != nil})
}

func (s *Server) handleRunJSONL(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	path := journal.File(runner.JournalDir(s.DataDir), id)
	if _, err := os.Stat(path); err != nil {
		writeError(w, http.StatusNotFound, "no run "+id)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+id+`.jsonl"`)
	journal.WriteTo(path, w)
}

// handleRunNow performs today's cycle immediately, whatever the clock says:
// the phone's way of seeing the machinery turn, and the operator's way of
// running a day that was missed. In dry-run it is harmless; in live it is
// a real trading decision, so it asks for the same confirmation flag the
// scheduler honours.
func (s *Server) handleRunNow(w http.ResponseWriter, r *http.Request) {
	if s.RunNow == nil {
		writeError(w, http.StatusConflict, "the server was started without a runner (no specs or bars directory)")
		return
	}
	var body struct {
		RunID string `json:"runId"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	id := body.RunID
	if id == "" {
		sess, err := sched.NextClose(time.Now())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		id = sess.RunID()
	}
	out, err := s.RunNow(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "run: "+err.Error())
		return
	}
	v, _ := s.runView(out.RunID)
	writeJSON(w, http.StatusOK, map[string]any{"run": v, "outcome": out})
}
