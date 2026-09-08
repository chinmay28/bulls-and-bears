package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/sched"
	"github.com/chinmay28/bulls-and-bears/server/internal/spec"
)

// Schedule re-validates every installed spec once a month, outside market
// hours, by re-running the study that produced it with the training cutoff
// it was produced with. The cutoff is held fixed on purpose: the parameters
// cannot drift, and the out-of-sample window only grows, so the question
// each run asks is the honest one — does it still hold with more data?
//
// A run that promotes rewrites the spec with a fresh generation date, and
// it stays armed. A run that does not leaves the spec alone: it expires at
// its TTL, the runtime refuses it, and the runner's targets for it go to
// zero. That is the only automatic disarm; the fast ones — the drawdown
// kill switch and the live-versus-paper divergence — stay in the risk gate,
// where they act on what is happening to the money.
type Schedule struct {
	Runner   *Runner
	SpecsDir string
	// DataDir is where the state file lives: <DataDir>/research/revalidate.json.
	DataDir string
	Log     *slog.Logger
	// Now is the clock; nil means time.Now. Sleep waits d or until ctx ends,
	// reporting false in the latter case; nil means a real wait. Tests set
	// both.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) bool
	// Interval is how often the schedule looks for due specs; zero means an hour.
	Interval time.Duration

	mu      sync.Mutex
	loaded  bool
	state   State
	running bool
}

// State is what the schedule remembers, on disk.
type State struct {
	Enabled bool `json:"enabled"`
	// LastRun is when each spec was last re-validated, by name.
	LastRun map[string]time.Time `json:"lastRun"`
	// History is the most recent re-validations, newest first.
	History []Record `json:"history"`
}

// Record is one re-validation of one spec.
type Record struct {
	Name    string    `json:"name"`
	Study   string    `json:"study"`
	JobID   string    `json:"jobId,omitempty"`
	At      time.Time `json:"at"`
	Status  Status    `json:"status"`
	Outcome *Outcome  `json:"outcome,omitempty"`
	Error   string    `json:"error,omitempty"`
}

// Status of the schedule as the API shows it.
type ScheduleView struct {
	State
	// Running says a re-validation pass is under way.
	Running bool `json:"running"`
	// Due lists the specs the next wake would re-run, were the market closed.
	Due []string `json:"due"`
}

const historyCap = 50

func (s *Schedule) file() string { return filepath.Join(s.DataDir, "research", "revalidate.json") }

func (s *Schedule) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Schedule) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// load reads the state once; a missing file is the default: enabled, no runs.
func (s *Schedule) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.state = State{Enabled: true, LastRun: map[string]time.Time{}}
	raw, err := os.ReadFile(s.file())
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		s.log().Warn("research: cannot read the re-validation state; starting enabled", "err", err)
		return
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		s.log().Warn("research: cannot parse the re-validation state; starting enabled", "err", err)
		return
	}
	if st.LastRun == nil {
		st.LastRun = map[string]time.Time{}
	}
	s.state = st
}

// save writes the state; the caller holds the lock.
func (s *Schedule) save() error {
	if err := os.MkdirAll(filepath.Dir(s.file()), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.file() + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.file())
}

// SetEnabled turns the schedule on or off. Off stops future passes; one
// under way finishes.
func (s *Schedule) SetEnabled(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	s.state.Enabled = on
	return s.save()
}

// View is the schedule's state for the API.
func (s *Schedule) View() ScheduleView {
	s.mu.Lock()
	s.load()
	v := ScheduleView{State: s.state, Running: s.running}
	v.LastRun = map[string]time.Time{}
	for k, t := range s.state.LastRun {
		v.LastRun[k] = t
	}
	v.History = append([]Record(nil), s.state.History...)
	if v.History == nil {
		v.History = []Record{}
	}
	s.mu.Unlock()
	for _, sp := range s.due(s.now(), false) {
		v.Due = append(v.Due, sp.Name)
	}
	if v.Due == nil {
		v.Due = []string{}
	}
	return v
}

// installed is every spec in the directory that parses, whether or not the
// runtime would run it today: an expired spec is exactly the one that needs
// re-validating.
func (s *Schedule) installed(now time.Time) []*spec.Spec {
	var out []*spec.Spec
	for _, l := range spec.LoadDir(s.SpecsDir, now) {
		if l.Spec != nil {
			out = append(out, l.Spec)
			continue
		}
		var refused *spec.Refused
		if errors.As(l.Err, &refused) && refused.Spec != nil {
			out = append(out, refused.Spec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// due is the installed specs not yet re-validated this calendar month (UTC),
// or every installed spec when force is set.
func (s *Schedule) due(now time.Time, force bool) []*spec.Spec {
	s.mu.Lock()
	s.load()
	last := map[string]time.Time{}
	for k, t := range s.state.LastRun {
		last[k] = t
	}
	s.mu.Unlock()
	y, m, _ := now.UTC().Date()
	var out []*spec.Spec
	for _, sp := range s.installed(now) {
		if !force {
			if t, ok := last[sp.Name]; ok {
				ly, lm, _ := t.UTC().Date()
				if ly == y && lm == m {
					continue
				}
			}
		}
		out = append(out, sp)
	}
	return out
}

// MarketClosed reports whether now is outside a trading session, with half
// an hour's margin after the close so the daily run has finished. Research
// fetches and computes for minutes at a time; it has no business doing so
// while the machine is deciding trades.
func MarketClosed(now time.Time) bool {
	sess, ok := sched.SessionOn(now.In(sched.NewYork))
	if !ok {
		return true
	}
	y, m, d := sess.Date.Date()
	open := time.Date(y, m, d, 9, 30, 0, 0, sched.NewYork)
	return now.Before(open) || now.After(sess.Close.Add(30*time.Minute))
}

// ForSpec is the study and options that re-validate a spec: the study that
// emits its strategy, on its universe, with its training cutoff.
func ForSpec(sp *spec.Spec) (Study, Options, error) {
	st, ok := ForStrategy(sp.Strategy)
	if !ok {
		return Study{}, Options{}, fmt.Errorf("research: no study produces %s", sp.Strategy)
	}
	opts := Options{Universe: append([]string(nil), sp.Universe...)}
	if st.DefaultTrainTo != "" {
		opts.TrainTo = sp.Provenance.TrainWindow.To.UTC().Format("2006-01-02")
	}
	return st, opts, nil
}

// Serve runs the schedule until ctx ends: every Interval it re-validates
// what is due, if the schedule is on and the market is closed.
func (s *Schedule) Serve(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = time.Hour
	}
	sleep := s.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) bool {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return false
			case <-t.C:
				return true
			}
		}
	}
	for {
		s.Pass(ctx, false)
		if !sleep(ctx, interval) {
			return
		}
	}
}

// Pass re-validates what is due, one spec at a time, and reports how many
// it ran. With force every installed spec is due; otherwise the schedule
// must be on and the market closed. A pass already under way, or a research
// job started from the phone, makes this one a no-op.
func (s *Schedule) Pass(ctx context.Context, force bool) int {
	now := s.now()
	s.mu.Lock()
	s.load()
	enabled := s.state.Enabled
	if s.running {
		s.mu.Unlock()
		return 0
	}
	if !force && (!enabled || !MarketClosed(now)) {
		s.mu.Unlock()
		return 0
	}
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	ran := 0
	for _, sp := range s.due(now, force) {
		if ctx.Err() != nil {
			return ran
		}
		rec := s.runOne(ctx, sp)
		s.mu.Lock()
		s.state.LastRun[sp.Name] = rec.At
		s.state.History = append([]Record{rec}, s.state.History...)
		if len(s.state.History) > historyCap {
			s.state.History = s.state.History[:historyCap]
		}
		if err := s.save(); err != nil {
			s.log().Error("research: cannot save the re-validation state", "err", err)
		}
		s.mu.Unlock()
		s.log().Info("research: re-validated", "spec", sp.Name, "status", rec.Status, "outcome", rec.Outcome, "err", rec.Error)
		ran++
	}
	return ran
}

// runOne re-runs one spec's study and waits for it.
func (s *Schedule) runOne(ctx context.Context, sp *spec.Spec) Record {
	rec := Record{Name: sp.Name, At: s.now().UTC()}
	st, opts, err := ForSpec(sp)
	if err != nil {
		rec.Status, rec.Error = Failed, err.Error()
		return rec
	}
	rec.Study = st.Name
	v, err := s.Runner.Start(Run, st.Name, opts)
	if err != nil {
		rec.Status, rec.Error = Failed, err.Error()
		return rec
	}
	rec.JobID = v.ID
	done, ok := s.Runner.Wait(ctx, v.ID)
	if !ok {
		rec.Status, rec.Error = Cancelled, "the server stopped before the study finished"
		return rec
	}
	rec.Status, rec.Outcome, rec.Error = done.Status, done.Outcome, done.Error
	return rec
}

// Wait blocks until the job ends or ctx does, returning the job's final
// view and whether it ended.
func (r *Runner) Wait(ctx context.Context, id string) (View, bool) {
	j := r.find(id)
	if j == nil {
		return View{}, false
	}
	select {
	case <-j.done:
		return j.snapshot(), true
	case <-ctx.Done():
		return j.snapshot(), false
	}
}
