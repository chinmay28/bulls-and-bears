package api

import "net/http"

// overviewView is the dashboard in one round trip. Until the paper book and
// the scheduler exist it carries the mode and the halt state, with the rest
// null so the app shows "nothing yet" rather than a zero that reads as a
// number.
type overviewView struct {
	Mode   string    `json:"mode"`
	Halted bool      `json:"halted"`
	Halt   *haltView `json:"halt"`
	// Book is null until there is a paper book to report.
	Book *bookView `json:"book"`
	// Strategies is every spec found, loaded or refused. Empty rather than
	// null so the app can map over it.
	Strategies []strategyView `json:"strategies"`
	// LastRun is null before the first run.
	LastRun *runView `json:"lastRun"`
}

type bookView struct {
	Equity       float64 `json:"equity"`
	DayChange    float64 `json:"dayChange"`
	SinceStart   float64 `json:"sinceStartPct"`
	DrawdownPct  float64 `json:"drawdownPct"`
	DailyLossPct float64 `json:"dailyLossPct"`
	OrdersToday  int     `json:"ordersToday"`
}

type strategyView struct {
	Name string `json:"name"`
	// Status is "armed" for a spec the runtime accepted, "refused" for one it
	// will not run, with Reason saying why.
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type runView struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
	Events int    `json:"events"`
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	halted, hv := s.haltView()
	writeJSON(w, http.StatusOK, overviewView{
		Mode:       "dry-run",
		Halted:     halted,
		Halt:       hv,
		Strategies: []strategyView{},
	})
}
