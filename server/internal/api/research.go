package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/chinmay28/bulls-and-bears/server/internal/research"
)

// This file is the phone's way to run research: the studies under
// research/scripts, with their environment set up on the machine and their
// output pointed at the runtime's own specs and bars. Nothing here can put a
// spec in front of a run that the script's own gate would not: a study
// promotes only when its out-of-sample Sharpe clears the floor, and the
// runtime then judges the file as it judges any other.

// researchView is the Research tab in one round trip.
type researchView struct {
	Env     research.Env     `json:"env"`
	Studies []research.Study `json:"studies"`
	// Current is the job running or last run in this process; null before
	// the first one.
	Current *research.View `json:"current"`
	// Recent is this process's earlier jobs, newest first.
	Recent []research.View `json:"recent"`
	// Logs is every job with a log on disk, newest first, this process's
	// and earlier ones'.
	Logs []string `json:"logs"`
	// Schedule is the monthly re-validation; null when the server has none.
	Schedule *research.ScheduleView `json:"schedule"`
}

// jobLogView is a job with the part of its log the client has not seen.
type jobLogView struct {
	Job research.View `json:"job"`
	Log string        `json:"log"`
	// Next is the offset to ask for next time.
	Next int `json:"next"`
}

func (s *Server) researchOr409(w http.ResponseWriter) bool {
	if s.Research == nil {
		writeError(w, http.StatusConflict, "the server was started without a research tree")
		return false
	}
	return true
}

func (s *Server) handleResearch(w http.ResponseWriter, r *http.Request) {
	if !s.researchOr409(w) {
		return
	}
	v := researchView{Env: s.Research.Env(), Studies: research.Studies(), Recent: s.Research.Recent(), Logs: s.Research.LogFiles()}
	if cur, ok := s.Research.Current(); ok {
		v.Current = &cur
	}
	if v.Recent == nil {
		v.Recent = []research.View{}
	}
	if v.Logs == nil {
		v.Logs = []string{}
	}
	if s.Schedule != nil {
		sv := s.Schedule.View()
		v.Schedule = &sv
	}
	writeJSON(w, http.StatusOK, v)
}

// handleResearchSchedule turns the monthly re-validation on or off.
func (s *Server) handleResearchSchedule(w http.ResponseWriter, r *http.Request) {
	if s.Schedule == nil {
		writeError(w, http.StatusConflict, "the server has no re-validation schedule")
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Enabled == nil {
		writeError(w, http.StatusBadRequest, "say whether the schedule is enabled")
		return
	}
	if err := s.Schedule.SetEnabled(*body.Enabled); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Log.Info("research re-validation schedule", "enabled", *body.Enabled)
	writeJSON(w, http.StatusOK, s.Schedule.View())
}

// handleResearchRevalidate re-validates every installed spec now, in the
// background, whatever the month or the market: the operator asked.
func (s *Server) handleResearchRevalidate(w http.ResponseWriter, r *http.Request) {
	if s.Schedule == nil {
		writeError(w, http.StatusConflict, "the server has no re-validation schedule")
		return
	}
	if s.Schedule.View().Running || (s.Research != nil && s.Research.Env().Busy) {
		writeError(w, http.StatusConflict, "a research job is already running; wait for it or cancel it")
		return
	}
	s.Log.Info("research re-validation of every installed spec started from the app")
	go s.Schedule.Pass(context.Background(), true)
	writeJSON(w, http.StatusAccepted, s.Schedule.View())
}

func (s *Server) handleResearchSetup(w http.ResponseWriter, r *http.Request) {
	if !s.researchOr409(w) {
		return
	}
	s.startResearch(w, research.Setup, "", research.Options{})
}

func (s *Server) handleResearchRun(w http.ResponseWriter, r *http.Request) {
	if !s.researchOr409(w) {
		return
	}
	var body struct {
		Study    string   `json:"study"`
		TrainTo  string   `json:"trainTo"`
		Universe []string `json:"universe"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Study) == "" {
		writeError(w, http.StatusBadRequest, "no study named")
		return
	}
	s.startResearch(w, research.Run, body.Study, research.Options{TrainTo: strings.TrimSpace(body.TrainTo), Universe: body.Universe})
}

func (s *Server) startResearch(w http.ResponseWriter, kind research.Kind, study string, opts research.Options) {
	v, err := s.Research.Start(kind, study, opts)
	switch {
	case errors.Is(err, research.ErrBusy):
		writeError(w, http.StatusConflict, "a research job is already running; wait for it or cancel it")
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.Log.Info("research job started", "id", v.ID, "kind", v.Kind, "study", v.Study, "trainTo", v.Options.TrainTo, "universe", v.Options.Universe)
	writeJSON(w, http.StatusAccepted, v)
}

func (s *Server) handleResearchJob(w http.ResponseWriter, r *http.Request) {
	if !s.researchOr409(w) {
		return
	}
	from := 0
	if q := r.URL.Query().Get("from"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "from must be a byte offset")
			return
		}
		from = n
	}
	v, text, err := s.Research.Get(r.PathValue("id"), from)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	next := from + len(text)
	if next > v.LogBytes {
		next = v.LogBytes
	}
	if from > v.LogBytes {
		// The client is ahead of a log it does not know was truncated in
		// memory; start it over from what is held.
		next = v.LogBytes
	}
	writeJSON(w, http.StatusOK, jobLogView{Job: v, Log: text, Next: next})
}

func (s *Server) handleResearchCancel(w http.ResponseWriter, r *http.Request) {
	if !s.researchOr409(w) {
		return
	}
	id := r.PathValue("id")
	if err := s.Research.Cancel(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.Log.Info("research job cancelled", "id", id)
	v, _, _ := s.Research.Get(id, 0)
	writeJSON(w, http.StatusOK, v)
}
