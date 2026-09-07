package api

import (
	"net/http"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/sched"
)

// overviewView is the dashboard in one round trip: the mode and halt state,
// the book and how close its guardrails are, every spec and its signal, the
// last run and the next.
type overviewView struct {
	Mode   string    `json:"mode"`
	Halted bool      `json:"halted"`
	Halt   *haltView `json:"halt"`
	// Book is null until there is a paper book to report, or when it cannot
	// be marked; BookError says which.
	Book      *bookSummaryView `json:"book"`
	BookError string           `json:"bookError,omitempty"`
	// Strategies is every spec found, armed or not. Empty rather than null.
	Strategies []strategyView `json:"strategies"`
	// LastRun is null before the first run.
	LastRun *runView `json:"lastRun"`
	// NextRun is when the scheduler fires next, null when it is not running.
	NextRun *nextRunView `json:"nextRun"`
	// Limits are the risk thresholds the meters are drawn against.
	Limits limitsView `json:"limits"`
}

type nextRunView struct {
	RunID string    `json:"runId"`
	At    time.Time `json:"at"`
	Early bool      `json:"earlyClose"`
}

type limitsView struct {
	DailyLossLimit     float64 `json:"dailyLossLimit"`
	DrawdownKillSwitch float64 `json:"drawdownKillSwitch"`
	MaxOrdersPerDay    int     `json:"maxOrdersPerDay"`
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	halted, hv := s.haltView()
	v := overviewView{
		Mode: "dry-run", Halted: halted, Halt: hv,
		Strategies: s.strategies(now),
		Limits:     limitsView{s.Risk.DailyLossLimit, s.Risk.DrawdownKillSwitch, s.Risk.MaxOrdersPerDay},
	}
	if s.SpecsDir == "" {
		v.Strategies = []strategyView{}
	}
	v.Book, v.BookError = s.bookSummary(r.Context(), now)
	if runs, err := s.runViews(1); err == nil && len(runs) > 0 {
		v.LastRun = &runs[0]
		if v.Book != nil {
			today, _ := sched.NextClose(now)
			if runs[0].RunID == today.RunID() {
				v.Book.OrdersToday = runs[0].Orders
			}
		}
	}
	if s.RunNow != nil {
		if sess, err := sched.NextClose(now); err == nil {
			v.NextRun = &nextRunView{RunID: sess.RunID(), At: sess.Close.Add(-s.RunOffset), Early: sess.Early}
		}
	}
	writeJSON(w, http.StatusOK, v)
}
