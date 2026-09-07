// Package journal is the append-only record of everything a run did.
//
// One JSONL file per run, one event per line, written before the thing it
// describes is done (docs/PLAN.md §4.5): a quote as it was seen, the targets
// the strategy asked for, the risk gate's decision on every intent, each
// order as it went out, each fill as it came back, and every error and halt.
// Nothing in a file is ever rewritten. The invariant the rest of the system
// is built on — no order is submitted without an allowed risk decision for
// the same intent — is checked here, over the file, after the fact.
package journal

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kind is what an event records.
type Kind string

const (
	KindQuote Kind = "quote"
	// KindStrategy records one spec's fate at the start of a run: data.name,
	// data.path, data.armed and, when it did not arm, data.reason. A run that
	// trades nothing is a run that refused every spec, and this is where it
	// says which and why.
	KindStrategy       Kind = "strategy"
	KindBars           Kind = "bars"
	KindTargets        Kind = "targets"
	KindRiskDecision   Kind = "risk_decision"
	KindOrderSubmitted Kind = "order_submitted"
	KindFill           Kind = "fill"
	KindError          Kind = "error"
	KindHalt           Kind = "halt"
	// KindRun brackets a run: data.status is "started" or "completed".
	KindRun Kind = "run"
)

// Event is one line. Data is whatever the kind carries, kept as raw JSON so
// a round trip through the file changes nothing. Allowed is set on risk
// decisions only; it is a pointer so "absent" and "false" stay different.
type Event struct {
	TS       time.Time       `json:"ts"`
	RunID    string          `json:"run_id"`
	Mode     string          `json:"mode"`
	Kind     Kind            `json:"kind"`
	IntentID string          `json:"intent_id,omitempty"`
	Allowed  *bool           `json:"allowed,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
}

// Decision builds a risk_decision event from its parts.
func Decision(intentID string, allowed bool, data any) Event {
	return Event{Kind: KindRiskDecision, IntentID: intentID, Allowed: &allowed, Data: mustJSON(data)}
}

// New builds an event of any kind with a data payload.
func New(kind Kind, intentID string, data any) Event {
	return Event{Kind: kind, IntentID: intentID, Data: mustJSON(data)}
}

func mustJSON(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(map[string]string{"unencodable": err.Error()})
	}
	return b
}

// Writer appends to one run's file.
type Writer struct {
	mu    sync.Mutex
	f     *os.File
	runID string
	mode  string
	now   func() time.Time
}

// File is the path of a run's journal under dir.
func File(dir, runID string) string { return filepath.Join(dir, runID+".jsonl") }

// Open appends to <dir>/<runID>.jsonl, creating the directory (0700) and the
// file (0600) as needed. Opening the same run twice appends; nothing is ever
// truncated.
func Open(dir, runID, mode string) (*Writer, error) {
	if runID == "" || strings.ContainsAny(runID, "/\\") {
		return nil, fmt.Errorf("journal: bad run id %q", runID)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(File(dir, runID), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Writer{f: f, runID: runID, mode: mode, now: time.Now}, nil
}

// Write appends one event, filling in the timestamp, run id and mode, and
// syncs it to disk before returning: an order goes out only after its
// decision is on disk, so the write must be durable, not buffered.
func (w *Writer) Write(ev Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ev.TS.IsZero() {
		ev.TS = w.now().UTC()
	}
	ev.RunID = w.runID
	ev.Mode = w.mode
	if ev.Kind == "" {
		return errors.New("journal: event has no kind")
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := w.f.Write(append(line, '\n')); err != nil {
		return err
	}
	return w.f.Sync()
}

// Close releases the file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

// Truncated reports a file whose last line was cut short — the process died
// mid-write. The events before it are still returned, and callers decide
// whether a partial tail matters to them.
type Truncated struct {
	Path string
	Line int
}

func (e *Truncated) Error() string {
	return fmt.Sprintf("journal: %s: line %d is not a complete event", e.Path, e.Line)
}

// Read parses one run's file. A bad last line yields the events before it
// and a *Truncated error; a bad line anywhere else is an error, since a
// journal is not supposed to have holes.
func Read(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var events []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			// Only a torn tail is tolerated: peek whether more follows.
			if sc.Scan() {
				return nil, fmt.Errorf("journal: %s: line %d: %w", path, n, err)
			}
			return events, &Truncated{Path: path, Line: n}
		}
		events = append(events, ev)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// ListRuns lists the run ids with a journal under dir, newest first (ids are
// dates, so lexical order is chronological).
func ListRuns(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".jsonl"))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids, nil
}

// Check enforces the invariant over a run's events, in order: every
// order_submitted must follow a risk_decision for the same intent with
// allowed true, and no order may follow a rejected decision. The first
// violation is the error.
func Check(events []Event) error {
	decided := map[string]bool{}
	for i, ev := range events {
		switch ev.Kind {
		case KindRiskDecision:
			if ev.IntentID == "" {
				return fmt.Errorf("journal: event %d: risk decision without an intent id", i)
			}
			decided[ev.IntentID] = ev.Allowed != nil && *ev.Allowed
		case KindOrderSubmitted:
			allowed, seen := decided[ev.IntentID]
			switch {
			case ev.IntentID == "":
				return fmt.Errorf("journal: event %d: order submitted without an intent id", i)
			case !seen:
				return fmt.Errorf("journal: event %d: order %s submitted with no risk decision before it", i, ev.IntentID)
			case !allowed:
				return fmt.Errorf("journal: event %d: order %s submitted after its risk decision rejected it", i, ev.IntentID)
			}
		}
	}
	return nil
}

// Summary is what a list of runs shows per run.
type Summary struct {
	RunID  string
	Mode   string
	Events int
	// Status is "completed", "halted", "failed" or "started" from the run
	// events, or "unknown" for a journal without them.
	Status    string
	Orders    int
	Fills     int
	Rejected  int
	Errors    int
	StartedAt time.Time
	EndedAt   time.Time
}

// Summarize reads a run's file and counts what happened in it.
func Summarize(path string) (Summary, error) {
	events, err := Read(path)
	var trunc *Truncated
	if err != nil && !errors.As(err, &trunc) {
		return Summary{}, err
	}
	s := Summary{Status: "unknown", Events: len(events)}
	for _, ev := range events {
		if s.RunID == "" {
			s.RunID, s.Mode, s.StartedAt = ev.RunID, ev.Mode, ev.TS
		}
		s.EndedAt = ev.TS
		switch ev.Kind {
		case KindOrderSubmitted:
			s.Orders++
		case KindFill:
			s.Fills++
		case KindRiskDecision:
			if ev.Allowed == nil || !*ev.Allowed {
				s.Rejected++
			}
		case KindError:
			s.Errors++
		case KindHalt:
			s.Status = "halted"
		case KindRun:
			var d struct {
				Status string `json:"status"`
			}
			if json.Unmarshal(ev.Data, &d) == nil && d.Status != "" {
				s.Status = d.Status
			}
		}
	}
	if trunc != nil && s.Status == "started" {
		s.Status = "failed"
	}
	return s, nil
}

// WriteTo copies a run's raw file to w, for download.
func WriteTo(path string, w io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}
