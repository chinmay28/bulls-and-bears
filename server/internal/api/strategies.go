package api

import (
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/backtest"
	"github.com/chinmay28/bulls-and-bears/server/internal/bars"
	"github.com/chinmay28/bulls-and-bears/server/internal/spec"
	"github.com/chinmay28/bulls-and-bears/server/internal/stage"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/pairs"
)

// strategyView is one spec as the phone shows it: what it is, whether the
// runtime will run it and why not, and where its signal sits right now.
type strategyView struct {
	Name     string             `json:"name"`
	Path     string             `json:"path"`
	Status   string             `json:"status"` // armed | refused | invalid
	Reason   string             `json:"reason,omitempty"`
	Strategy string             `json:"strategy,omitempty"`
	Universe []string           `json:"universe,omitempty"`
	Params   map[string]float64 `json:"params,omitempty"`
	Sizing   *sizingView        `json:"sizing,omitempty"`
	// Provenance is null for an invalid file.
	Provenance *provenanceView `json:"provenance,omitempty"`
	// FreshDays is how long the TTL has left; Expires when it runs out.
	FreshDays float64    `json:"freshDays"`
	Expires   *time.Time `json:"expires,omitempty"`
	// Stage is where the spec may trade: "paper" until the operator promotes
	// it to "live". In dry-run every spec trades on paper regardless.
	Stage string `json:"stage"`
	// Signal is the strategy's current reading, null when bars are missing.
	Signal *signalView `json:"signal"`
	// SignalError says why Signal is null, when it is.
	SignalError string `json:"signalError,omitempty"`
}

type sizingView struct {
	GrossLeverage        float64 `json:"grossLeverage"`
	MaxNotionalPerLegUSD float64 `json:"maxNotionalPerLegUsd"`
}

type provenanceView struct {
	TrainFrom      string  `json:"trainFrom"`
	TrainTo        string  `json:"trainTo"`
	TestFrom       string  `json:"testFrom"`
	TestTo         string  `json:"testTo"`
	OOSSharpe      float64 `json:"oosSharpe"`
	OOSMaxDrawdown float64 `json:"oosMaxDrawdown"`
	CommissionUSD  float64 `json:"commissionUsd"`
	SlippageBps    float64 `json:"slippageBps"`
	ResearchGitSHA string  `json:"researchGitSha"`
	GeneratedAt    string  `json:"generatedAt"`
	TTLDays        int     `json:"ttlDays"`
}

// signalView is the pairs strategy's last bar: the z-score against the
// band, and the spread position it implies.
type signalView struct {
	Date     string             `json:"date"`
	Z        float64            `json:"z"`
	ZDefined bool               `json:"zDefined"`
	State    int                `json:"state"`
	EntryZ   float64            `json:"entryZ"`
	ExitZ    float64            `json:"exitZ"`
	Weights  map[string]float64 `json:"weights"`
	LastBar  string             `json:"lastBar"`
	Stale    bool               `json:"stale"`
}

// strategies loads every spec and reads each armed one's signal from the
// bars on disk.
func (s *Server) strategies(now time.Time) []strategyView {
	out := []strategyView{}
	stages, stageErr := stage.Load(s.DataDir)
	if stageErr != nil {
		s.Log.Warn("api: cannot read the stages file; showing every spec as paper", "err", stageErr)
	}
	for _, l := range spec.LoadDir(s.SpecsDir, now) {
		v := strategyView{Path: filepath.Base(l.Path), Name: nameOf(l), Stage: string(stage.Of(stages, nameOf(l)))}
		var refused *spec.Refused
		var invalid *spec.Invalid
		switch {
		case errors.As(l.Err, &refused):
			v.Status, v.Reason = "refused", refused.Reason
		case errors.As(l.Err, &invalid):
			v.Status, v.Reason = "invalid", invalid.Reason
		case l.Err != nil:
			v.Status, v.Reason = "invalid", l.Err.Error()
		default:
			v.Status = "armed"
		}
		if l.Spec != nil {
			sp := l.Spec
			v.Name, v.Strategy, v.Universe, v.Params = sp.Name, sp.Strategy, sp.Universe, sp.Params
			v.Sizing = &sizingView{sp.Sizing.GrossLeverage, sp.Sizing.MaxNotionalPerLegUSD}
			p := sp.Provenance
			v.Provenance = &provenanceView{
				TrainFrom: p.TrainWindow.From.Format("2006-01-02"), TrainTo: p.TrainWindow.To.Format("2006-01-02"),
				TestFrom: p.TestWindow.From.Format("2006-01-02"), TestTo: p.TestWindow.To.Format("2006-01-02"),
				OOSSharpe: p.OOSSharpe, OOSMaxDrawdown: p.OOSMaxDrawdown,
				CommissionUSD: p.CostModel.CommissionUSD, SlippageBps: p.CostModel.SlippageBps,
				ResearchGitSHA: p.ResearchGitSHA, GeneratedAt: p.GeneratedAt.UTC().Format(time.RFC3339), TTLDays: p.TTLDays,
			}
			exp := spec.Expires(sp)
			v.Expires = &exp
			v.FreshDays = spec.FreshFor(sp, now).Hours() / 24
			if v.Status == "armed" {
				v.Signal, v.SignalError = s.signal(sp, now)
			}
		}
		out = append(out, v)
	}
	return out
}

// nameOf is the spec's name, or the file's when the spec did not parse.
func nameOf(l spec.Loaded) string {
	if l.Spec != nil {
		return l.Spec.Name
	}
	base := filepath.Base(l.Path)
	return base[:len(base)-len(filepath.Ext(base))]
}

// signal replays the strategy over the bars on disk and reports its last
// bar. Only the pairs strategy has a z-score to show; others get the weights.
func (s *Server) signal(sp *spec.Spec, now time.Time) (*signalView, string) {
	hist, err := s.history(sp.Universe)
	if err != nil {
		return nil, err.Error()
	}
	st, err := strategy.New(sp.Strategy, sp.Universe, sp.Params, sp.Sizing)
	if err != nil {
		return nil, err.Error()
	}
	last := ""
	stale := false
	for _, series := range hist {
		d := series[len(series)-1].Date.Format("2006-01-02")
		if last == "" || d < last {
			last = d
		}
		if bars.Stale(series, now, 3) {
			stale = true
		}
	}
	if p, ok := st.(*pairs.Pairs); ok {
		sig, err := p.Signals(hist)
		if err != nil {
			return nil, err.Error()
		}
		if len(sig) == 0 {
			return nil, "no aligned bars"
		}
		z := sig[len(sig)-1]
		return &signalView{Date: z.Date.Format("2006-01-02"), Z: z.Z, ZDefined: z.ZDefined, State: z.State,
			EntryZ: p.Params().EntryZ, ExitZ: p.Params().ExitZ, Weights: z.Weights, LastBar: last, Stale: stale}, ""
	}
	w, err := st.Targets(hist, nil)
	if err != nil {
		return nil, err.Error()
	}
	return &signalView{Date: last, Weights: w, LastBar: last, Stale: stale}, ""
}

// history reads the bars for a universe from the bars directory.
func (s *Server) history(universe []string) (map[string][]bars.Bar, error) {
	hist := map[string][]bars.Bar{}
	for _, sym := range universe {
		series, err := bars.Read(filepath.Join(s.BarsDir, sym+".parquet"))
		if err != nil {
			return nil, err
		}
		if len(series) == 0 {
			return nil, errors.New("no bars for " + sym)
		}
		hist[sym] = series
	}
	return hist, nil
}

// handleSetStage promotes a spec to live or returns it to paper. It is the
// operator's decision, recorded in the data directory: research cannot make
// it and a re-validation cannot unmake it. It changes nothing in dry-run,
// where every spec trades on paper; in live mode the runner arms only
// promoted specs.
func (s *Server) handleSetStage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Stage string `json:"stage"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	st, err := stage.Parse(body.Stage)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now()
	found := false
	for _, v := range s.strategies(now) {
		if v.Name == name {
			found = true
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "no spec named "+name)
		return
	}
	if err := stage.Set(s.DataDir, name, st); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Log.Info("spec stage set", "name", name, "stage", st)
	for _, v := range s.strategies(now) {
		if v.Name == name {
			writeJSON(w, http.StatusOK, v)
			return
		}
	}
}

func (s *Server) handleListStrategies(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.strategies(time.Now()))
}

func (s *Server) handleGetStrategy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	for _, v := range s.strategies(time.Now()) {
		if v.Name == name {
			writeJSON(w, http.StatusOK, v)
			return
		}
	}
	writeError(w, http.StatusNotFound, "no spec named "+name)
}

// backtestView is what a backtest from the phone returns: the numbers and a
// curve small enough to draw.
type backtestView struct {
	Name   string        `json:"name"`
	From   string        `json:"from"`
	To     string        `json:"to"`
	Bars   int           `json:"bars"`
	Trades int           `json:"trades"`
	Sharpe *float64      `json:"sharpe"`
	MaxDD  float64       `json:"maxDrawdown"`
	DDDays int           `json:"maxDrawdownDuration"`
	Return float64       `json:"totalReturn"`
	Equity []equityPoint `json:"equity"`
	// Spec is what the spec claimed out of sample, for the comparison.
	SpecSharpe float64 `json:"specSharpe"`
}

type equityPoint struct {
	Date   string  `json:"date"`
	Equity float64 `json:"equity"`
}

// handleBacktest replays the named spec over the bars on disk from its test
// window's start, with its own cost model: the same run `bnb backtest` does.
func (s *Server) handleBacktest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var found *spec.Spec
	for _, l := range spec.LoadDir(s.SpecsDir, time.Now()) {
		if l.Spec != nil && l.Spec.Name == name {
			found = l.Spec
		}
	}
	if found == nil {
		writeError(w, http.StatusNotFound, "no loadable spec named "+name)
		return
	}
	hist, err := s.history(found.Universe)
	if err != nil {
		writeError(w, http.StatusConflict, "bars: "+err.Error())
		return
	}
	st, err := strategy.New(found.Strategy, found.Universe, found.Params, found.Sizing)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	res, err := backtest.StrategyFrom(st, hist, found.Provenance.TestWindow.From, backtest.Costs{
		CommissionUSD: found.Provenance.CostModel.CommissionUSD, SlippageBps: found.Provenance.CostModel.SlippageBps,
	})
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	v := backtestView{
		Name: name, Bars: len(res.Equity), Trades: len(res.Trades),
		MaxDD: res.Summary.MaxDrawdown, DDDays: res.Summary.MaxDrawdownDuration, Return: res.Summary.TotalReturn,
		SpecSharpe: found.Provenance.OOSSharpe,
	}
	if res.SummaryErr == nil {
		sh := res.Summary.Sharpe
		v.Sharpe = &sh
	}
	if n := len(res.Equity); n > 0 {
		v.From = res.Equity[0].Date.Format("2006-01-02")
		v.To = res.Equity[n-1].Date.Format("2006-01-02")
		step := 1
		if n > 400 {
			step = (n + 399) / 400
		}
		for i := 0; i < n; i += step {
			v.Equity = append(v.Equity, equityPoint{res.Equity[i].Date.Format("2006-01-02"), res.Equity[i].Equity})
		}
		if (n-1)%step != 0 {
			v.Equity = append(v.Equity, equityPoint{res.Equity[n-1].Date.Format("2006-01-02"), res.Equity[n-1].Equity})
		}
	}
	writeJSON(w, http.StatusOK, v)
}

// barsView is one symbol's file as Settings lists it.
type barsView struct {
	Symbol string `json:"symbol"`
	Bars   int    `json:"bars"`
	First  string `json:"first,omitempty"`
	Last   string `json:"last,omitempty"`
	Source string `json:"source,omitempty"`
	Stale  bool   `json:"stale"`
	Error  string `json:"error,omitempty"`
}

// handleBars reports the bars on disk: every symbol a spec names, and every
// symbol the directory holds a file for. The second half is what makes the
// screen useful before there is a spec — bars fetched ahead of one are the
// head start, not clutter, and a run that will fail on stale data says so
// here first.
func (s *Server) handleBars(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	out := []barsView{}
	for _, sym := range s.knownSymbols(now) {
		v := barsView{Symbol: sym}
		series, err := bars.Read(filepath.Join(s.BarsDir, sym+".parquet"))
		switch {
		case err != nil:
			v.Error = err.Error()
			v.Stale = true
		case len(series) == 0:
			v.Error = "empty"
			v.Stale = true
		default:
			v.Bars = len(series)
			v.First = series[0].Date.Format("2006-01-02")
			v.Last = series[len(series)-1].Date.Format("2006-01-02")
			v.Source = series[len(series)-1].Source
			v.Stale = bars.Stale(series, now, 3)
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}
