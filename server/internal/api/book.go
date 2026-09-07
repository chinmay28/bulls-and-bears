package api

import (
	"context"
	"net/http"
	"time"
)

// bookView is the paper account as the Book tab shows it.
type bookView struct {
	Mode         string         `json:"mode"`
	StartingCash float64        `json:"startingCash"`
	Cash         float64        `json:"cash"`
	Equity       float64        `json:"equity"`
	OpenedAt     time.Time      `json:"openedAt"`
	Positions    []positionView `json:"positions"`
	OpenOrders   []orderView    `json:"openOrders"`
	Fills        []fillView     `json:"fills"`
	History      []dayView      `json:"history"`
	// Gross is Σ|market value|, and GrossOfEquity the fraction of equity it is.
	Gross         float64 `json:"gross"`
	GrossOfEquity float64 `json:"grossOfEquity"`
}

type positionView struct {
	Symbol        string  `json:"symbol"`
	Qty           float64 `json:"qty"`
	AvgCost       float64 `json:"avgCost"`
	Mark          float64 `json:"mark"`
	MarketValue   float64 `json:"marketValue"`
	UnrealizedPnL float64 `json:"unrealizedPnl"`
}

type orderView struct {
	ID       string    `json:"id"`
	IntentID string    `json:"intentId"`
	Symbol   string    `json:"symbol"`
	Side     string    `json:"side"`
	Qty      float64   `json:"qty"`
	Limit    float64   `json:"limit"`
	PlacedAt time.Time `json:"placedAt"`
}

type fillView struct {
	OrderID  string    `json:"orderId"`
	IntentID string    `json:"intentId"`
	Symbol   string    `json:"symbol"`
	Qty      float64   `json:"qty"`
	Price    float64   `json:"price"`
	At       time.Time `json:"at"`
}

type dayView struct {
	Day    string  `json:"day"`
	Open   float64 `json:"open"`
	Equity float64 `json:"equity"`
}

// handleBook marks the paper book and lists what it holds and did.
func (s *Server) handleBook(w http.ResponseWriter, r *http.Request) {
	if s.Book == nil {
		writeError(w, http.StatusConflict, "no paper book: the server was started without one")
		return
	}
	ctx := r.Context()
	snap, err := s.Book.Snapshot(ctx)
	if err != nil {
		writeError(w, http.StatusConflict, "could not mark the book: "+err.Error())
		return
	}
	v := bookView{Mode: "dry-run", StartingCash: snap.StartingCash, Cash: snap.Cash, Equity: snap.Equity, OpenedAt: snap.OpenedAt,
		Positions: []positionView{}, OpenOrders: []orderView{}, Fills: []fillView{}, History: []dayView{}}
	for _, p := range snap.Positions {
		v.Positions = append(v.Positions, positionView{p.Symbol, p.Qty, p.AvgCost, p.Mark, p.MarketValue, p.UnrealizedPnL})
		if p.MarketValue < 0 {
			v.Gross -= p.MarketValue
		} else {
			v.Gross += p.MarketValue
		}
	}
	if snap.Equity > 0 {
		v.GrossOfEquity = v.Gross / snap.Equity
	}
	for _, o := range snap.OpenOrders {
		v.OpenOrders = append(v.OpenOrders, orderView{o.ID, o.IntentID, o.Symbol, o.Side.String(), o.Qty, o.Limit, o.PlacedAt})
	}
	fills, err := s.Book.Fills(ctx, time.Time{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "fills: "+err.Error())
		return
	}
	// Newest first, and only the last fifty: the phone shows a list, not a ledger.
	for i := len(fills) - 1; i >= 0 && len(v.Fills) < 50; i-- {
		f := fills[i]
		v.Fills = append(v.Fills, fillView{f.OrderID, f.IntentID, f.Symbol, f.Qty, f.Price, f.At})
	}
	hist, err := s.Book.EquityHistory(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "history: "+err.Error())
		return
	}
	for _, d := range hist {
		v.History = append(v.History, dayView{d.Day.Format("2006-01-02"), d.Open, d.Equity})
	}
	writeJSON(w, http.StatusOK, v)
}

// bookSummary is the overview's slice of the book: the money and how close
// the guardrails are. nil with a reason when the book cannot be marked.
func (s *Server) bookSummary(ctx context.Context, now time.Time) (*bookSummaryView, string) {
	if s.Book == nil {
		return nil, "no paper book"
	}
	snap, err := s.Book.Snapshot(ctx)
	if err != nil {
		return nil, err.Error()
	}
	sod, err := s.Book.StartOfDayEquity(ctx, now)
	if err != nil {
		return nil, err.Error()
	}
	hw, err := s.Book.HighWaterEquity(ctx)
	if err != nil {
		return nil, err.Error()
	}
	v := &bookSummaryView{Equity: snap.Equity, StartingCash: snap.StartingCash, StartOfDay: sod, HighWater: hw, OpenedAt: snap.OpenedAt}
	v.DayChange = snap.Equity - sod
	if snap.StartingCash > 0 {
		v.SinceStartPct = snap.Equity/snap.StartingCash - 1
	}
	if hw > 0 && snap.Equity < hw {
		v.DrawdownPct = 1 - snap.Equity/hw
	}
	if sod > 0 && snap.Equity < sod {
		v.DailyLossPct = 1 - snap.Equity/sod
	}
	for _, p := range snap.Positions {
		v.Positions++
		if p.MarketValue < 0 {
			v.Gross -= p.MarketValue
		} else {
			v.Gross += p.MarketValue
		}
	}
	return v, ""
}

type bookSummaryView struct {
	Equity        float64   `json:"equity"`
	StartingCash  float64   `json:"startingCash"`
	StartOfDay    float64   `json:"startOfDay"`
	HighWater     float64   `json:"highWater"`
	DayChange     float64   `json:"dayChange"`
	SinceStartPct float64   `json:"sinceStartPct"`
	DrawdownPct   float64   `json:"drawdownPct"`
	DailyLossPct  float64   `json:"dailyLossPct"`
	OrdersToday   int       `json:"ordersToday"`
	Positions     int       `json:"positions"`
	Gross         float64   `json:"gross"`
	OpenedAt      time.Time `json:"openedAt"`
}
