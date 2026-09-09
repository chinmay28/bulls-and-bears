package api

import (
	"net/http"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/rotation"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/xlksata"
)

// rotationView is the XLK/SATA rotation as the app shows it.
//
// It is read-only, and that is a property rather than a shortfall: the
// rotation is a separate process holding a broker connection and the data
// directory's lock, so the server has no way to place an order for it and no
// business taking the lock to find out what it is doing. What the app can
// do — see the lot, watch the combined figure against its target, and stop
// the whole thing — it does. The halt marker the Stop button writes is the
// same one `bnb rotate` reads before every cycle.
type rotationView struct {
	// Configured is false when the ledger does not exist: the rotation has
	// never run in this data directory.
	Configured bool   `json:"configured"`
	Mode       string `json:"mode"`
	// Running and HolderPID describe the process that holds the lock, read
	// from the lock file rather than by trying to take it.
	Running   bool `json:"running"`
	HolderPID int  `json:"holderPid"`

	Risk string `json:"risk"`
	Park string `json:"park"`

	EntryPrice float64   `json:"entryPrice"`
	EntryDate  time.Time `json:"entryDate"`
	Basis      float64   `json:"basis"`
	OptionPnL  float64   `json:"optionPnl"`
	Dividends  float64   `json:"dividends"`
	Costs      float64   `json:"costs"`
	// Banked is everything the combined figure counts except the shares'
	// own move, which needs a quote the server does not have.
	Banked float64 `json:"banked"`

	QuickTargetPct    float64 `json:"quickTargetPct"`
	RecoveryTargetPct float64 `json:"recoveryTargetPct"`
	// RecoveryTargetUSD is what the lot has to make, in dollars.
	RecoveryTargetUSD float64 `json:"recoveryTargetUsd"`
	// BreakEvenPrice is the share price at which the lot reaches the
	// recovery target, given what it has banked. Zero when flat.
	BreakEvenPrice float64 `json:"breakEvenPrice"`

	ShortCall *rotationCallView    `json:"shortCall"`
	Pending   *rotationPendingView `json:"pending"`

	OrdersToday      int     `json:"ordersToday"`
	StartOfDayEquity float64 `json:"startOfDayEquity"`
	HighWaterEquity  float64 `json:"highWaterEquity"`
	DailyLimitHits   int     `json:"dailyLimitHits"`
	MarksDay         string  `json:"marksDay"`

	UpdatedAt time.Time           `json:"updatedAt"`
	History   []rotationEntryView `json:"history"`
	Schedule  []rotationPhaseView `json:"schedule"`
	Rules     []rotationRuleView  `json:"rules"`
}

type rotationCallView struct {
	OptionID   string    `json:"optionId"`
	Strike     float64   `json:"strike"`
	Expiration time.Time `json:"expiration"`
	Credit     float64   `json:"credit"`
	// AssignedPct is what the lot returns if it is called away here.
	AssignedPct float64 `json:"assignedPct"`
}

type rotationPendingView struct {
	OrderID  string    `json:"orderId"`
	Kind     string    `json:"kind"`
	Symbol   string    `json:"symbol"`
	Strike   float64   `json:"strike"`
	PlacedAt time.Time `json:"placedAt"`
}

type rotationEntryView struct {
	At        time.Time `json:"at"`
	Mode      string    `json:"mode"`
	Entry     float64   `json:"entry"`
	OptionPnL float64   `json:"optionPnl"`
	Strike    float64   `json:"strike"`
	Pending   string    `json:"pending"`
}

// rotationPhaseView is one of the day's decision points, in both the zone
// the rules are written in and the one a cron would use.
type rotationPhaseView struct {
	Name string `json:"name"`
	PT   string `json:"pt"`
	UTC  string `json:"utc"`
	Cron string `json:"cron"`
	What string `json:"what"`
}

type rotationRuleView struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// handleRotation reports the rotation's book without touching its lock.
func (s *Server) handleRotation(w http.ResponseWriter, r *http.Request) {
	cfg := xlksata.Defaults()
	v := rotationView{
		Risk: xlksata.Risk, Park: xlksata.Park,
		QuickTargetPct:    cfg.QuickTarget,
		RecoveryTargetPct: cfg.RecoveryTarget,
		History:           []rotationEntryView{},
		Schedule:          rotationSchedule(),
		Rules:             rotationRules(cfg),
	}
	if pid, ok := rotation.Holder(s.DataDir); ok {
		v.HolderPID, v.Running = pid, rotation.Alive(pid)
	}

	books, err := rotation.History(s.DataDir)
	if err != nil {
		writeError(w, http.StatusConflict, "the rotation ledger could not be read: "+err.Error())
		return
	}
	if len(books) == 0 {
		v.Mode = "flat"
		writeJSON(w, http.StatusOK, v)
		return
	}
	v.Configured = true

	book := books[len(books)-1]
	st := book.State
	v.Mode = st.Mode.String()
	v.UpdatedAt = book.At
	v.EntryPrice, v.EntryDate = st.EntryPrice, st.EntryDate
	v.OptionPnL, v.Dividends, v.Costs = st.OptionPnL, st.Dividends, st.Costs
	v.Banked = st.OptionPnL + st.Dividends - st.Costs
	v.OrdersToday = book.Marks.OrdersToday
	v.StartOfDayEquity, v.HighWaterEquity = book.Marks.StartOfDayEquity, book.Marks.HighWaterEquity
	v.DailyLimitHits, v.MarksDay = book.Marks.DailyLimitHits, book.Marks.Day

	if st.Open() {
		lot := float64(cfg.LotShares)
		v.Basis = lot * st.EntryPrice
		v.RecoveryTargetUSD = v.Basis * cfg.RecoveryTarget
		// What the shares have to be worth for the combined figure to reach
		// the target, given what is already banked.
		v.BreakEvenPrice = st.EntryPrice + (v.RecoveryTargetUSD-v.Banked)/lot
	}
	if c := st.ShortCall; c != nil {
		call := &rotationCallView{OptionID: c.OptionID, Strike: c.Strike, Expiration: c.Expiration, Credit: c.Credit}
		if v.Basis > 0 {
			lot := float64(cfg.LotShares)
			call.AssignedPct = (lot*(c.Strike-st.EntryPrice) + v.Banked) / v.Basis
		}
		v.ShortCall = call
	}
	if p := book.Pending; p != nil {
		v.Pending = &rotationPendingView{
			OrderID: p.OrderID, Kind: p.Kind.String(), Symbol: p.Symbol,
			Strike: p.Strike, PlacedAt: p.PlacedAt,
		}
	}

	// Newest first, and only the last fifty: the phone shows a list, not a
	// ledger.
	for i := len(books) - 1; i >= 0 && len(v.History) < 50; i-- {
		b := books[i]
		e := rotationEntryView{At: b.At, Mode: b.State.Mode.String(), Entry: b.State.EntryPrice, OptionPnL: b.State.OptionPnL}
		if c := b.State.ShortCall; c != nil {
			e.Strike = c.Strike
		}
		if p := b.Pending; p != nil {
			e.Pending = p.Kind.String()
		}
		v.History = append(v.History, e)
	}
	writeJSON(w, http.StatusOK, v)
}

// rotationSchedule is the day's two decision points. The UTC column is what
// a cron needs and is not a conversion of the PT column: cron cannot follow
// US daylight saving, so these times hold the gap between the phases and
// stay inside the session under either offset instead (docs/DISCOVERY.md).
func rotationSchedule() []rotationPhaseView {
	return []rotationPhaseView{
		{
			Name: "Entry", PT: "07:12", UTC: "14:45", Cron: "45 14 * * 1-5",
			What: "Open 100 " + xlksata.Risk + " out of " + xlksata.Park + ", if the book is flat.",
		},
		{
			Name: "Review", PT: "12:07", UTC: "19:40", Cron: "40 19 * * 1-5",
			What: "Sell at +0.15% or better; otherwise enter recovery.",
		},
		{
			Name: "Manage", PT: "hourly", UTC: "hourly", Cron: "0 15-20 * * 1-5",
			What: "Work the recovery, and sweep idle cash into " + xlksata.Park + ".",
		},
	}
}

// rotationRules is the strategy's numbers, so the app states the thing it is
// watching rather than assuming the reader remembers it.
func rotationRules(cfg xlksata.Config) []rotationRuleView {
	return []rotationRuleView{
		{"Lot", "exactly 100 shares, never a second"},
		{"Quick target", "+0.15% at the review"},
		{"Recovery target", "+1.00% combined on the original purchase value"},
		{"Combined profit", "share P&L + realised option cash + dividends − costs"},
		{"Covered call", "2–7 DTE, delta 0.20–0.30, strike at or above the entry"},
		{"Assignment test", "being called away must still return +1.00%"},
		{"Margin", "never: every buy is capped at settled cash"},
	}
}
