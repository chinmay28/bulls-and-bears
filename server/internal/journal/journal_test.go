package journal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTest(t *testing.T) (*Writer, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "journal")
	w, err := Open(dir, "2026-09-04", "dry-run")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w, dir
}

func TestWriteThenReadRoundTrip(t *testing.T) {
	w, dir := openTest(t)
	w.now = func() time.Time { return time.Date(2026, 9, 4, 19, 50, 1, 0, time.UTC) }
	events := []Event{
		New(KindRun, "", map[string]string{"status": "started"}),
		New(KindQuote, "", map[string]any{"symbol": "GLD", "bid": 231.08, "ask": 231.12}),
		Decision("a1f3", true, map[string]string{"reason": "within limits"}),
		New(KindOrderSubmitted, "a1f3", map[string]string{"order_id": "paper-1"}),
		New(KindFill, "a1f3", map[string]float64{"qty": 12.4, "price": 231.10}),
		New(KindRun, "", map[string]string{"status": "completed"}),
	}
	for _, ev := range events {
		if err := w.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Read(File(dir, "2026-09-04"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(events) {
		t.Fatalf("read %d events, want %d", len(got), len(events))
	}
	for i, ev := range got {
		if ev.RunID != "2026-09-04" || ev.Mode != "dry-run" || ev.Kind != events[i].Kind || ev.IntentID != events[i].IntentID {
			t.Errorf("event %d = %+v", i, ev)
		}
		if !ev.TS.Equal(time.Date(2026, 9, 4, 19, 50, 1, 0, time.UTC)) {
			t.Errorf("event %d ts = %v", i, ev.TS)
		}
		if string(ev.Data) != string(events[i].Data) {
			t.Errorf("event %d data = %s, want %s", i, ev.Data, events[i].Data)
		}
	}
	if got[2].Allowed == nil || !*got[2].Allowed {
		t.Error("allowed flag lost in the round trip")
	}
	if got[1].Allowed != nil {
		t.Error("a quote should not carry an allowed flag")
	}
	if err := Check(got); err != nil {
		t.Errorf("Check: %v", err)
	}

	fi, err := os.Stat(File(dir, "2026-09-04"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, err %v; want 0600", fi.Mode(), err)
	}
	if di, err := os.Stat(dir); err != nil || di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, err %v; want 0700", di.Mode(), err)
	}
}

func TestConcurrentWritesStayWholeLines(t *testing.T) {
	w, dir := openTest(t)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := w.Write(New(KindQuote, "", map[string]int{"i": i, "pad": 1 << 20})); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	got, err := Read(File(dir, "2026-09-04"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 50 {
		t.Errorf("read %d events, want 50", len(got))
	}
}

func TestTruncatedTailIsReported(t *testing.T) {
	w, dir := openTest(t)
	w.Write(New(KindRun, "", map[string]string{"status": "started"}))
	w.Write(Decision("x", true, nil))
	path := File(dir, "2026-09-04")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"ts":"2026-09-04T19:50:02Z","kind":"order_sub`)
	f.Close()

	got, err := Read(path)
	var trunc *Truncated
	if !errors.As(err, &trunc) || trunc.Line != 3 {
		t.Fatalf("err = %v, want *Truncated at line 3", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d events before the torn line, want 2", len(got))
	}
	s, err := Summarize(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != "failed" || s.Events != 2 {
		t.Errorf("summary = %+v, want a failed run of 2 events", s)
	}
}

func TestBadLineInTheMiddleIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.jsonl")
	os.WriteFile(path, []byte("{\"kind\":\"quote\"}\nnot json\n{\"kind\":\"quote\"}\n"), 0o600)
	if _, err := Read(path); err == nil || strings.Contains(err.Error(), "not a complete") {
		t.Fatalf("err = %v, want a hard parse error", err)
	}
}

func TestCheck(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name   string
		events []Event
		want   string // substring of the error, "" for ok
	}{
		{"empty", nil, ""},
		{"allowed then submitted", []Event{
			{Kind: KindRiskDecision, IntentID: "a", Allowed: &yes},
			{Kind: KindOrderSubmitted, IntentID: "a"},
		}, ""},
		{"submitted with no decision", []Event{
			{Kind: KindOrderSubmitted, IntentID: "a"},
		}, "no risk decision"},
		{"submitted after rejection", []Event{
			{Kind: KindRiskDecision, IntentID: "a", Allowed: &no},
			{Kind: KindOrderSubmitted, IntentID: "a"},
		}, "rejected it"},
		{"decision for a different intent", []Event{
			{Kind: KindRiskDecision, IntentID: "a", Allowed: &yes},
			{Kind: KindOrderSubmitted, IntentID: "b"},
		}, "no risk decision"},
		{"decision after the order", []Event{
			{Kind: KindOrderSubmitted, IntentID: "a"},
			{Kind: KindRiskDecision, IntentID: "a", Allowed: &yes},
		}, "no risk decision"},
		{"decision without allowed flag counts as rejected", []Event{
			{Kind: KindRiskDecision, IntentID: "a"},
			{Kind: KindOrderSubmitted, IntentID: "a"},
		}, "rejected it"},
		{"order without intent id", []Event{
			{Kind: KindOrderSubmitted},
		}, "without an intent id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Check(c.events)
			if c.want == "" && err != nil {
				t.Fatalf("Check = %v, want ok", err)
			}
			if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Fatalf("Check = %v, want an error mentioning %q", err, c.want)
			}
		})
	}
}

func TestListRunsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"2026-09-02", "2026-09-04", "2026-09-03"} {
		w, err := Open(dir, id, "dry-run")
		if err != nil {
			t.Fatal(err)
		}
		w.Close()
	}
	os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o600)
	ids, err := ListRuns(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "2026-09-04,2026-09-03,2026-09-02" {
		t.Errorf("ids = %v", ids)
	}
	if ids, err := ListRuns(filepath.Join(dir, "missing")); err != nil || ids != nil {
		t.Errorf("missing dir: ids = %v, err = %v; want none and no error", ids, err)
	}
}

func TestOpenRefusesAPathInTheRunID(t *testing.T) {
	if _, err := Open(t.TempDir(), "../etc", "dry-run"); err == nil {
		t.Fatal("a run id with a path separator must be refused")
	}
}

func TestSummarizeCounts(t *testing.T) {
	w, dir := openTest(t)
	w.Write(New(KindRun, "", map[string]string{"status": "started"}))
	w.Write(Decision("a", true, nil))
	w.Write(Decision("b", false, map[string]string{"reason": "stale quote"}))
	w.Write(New(KindOrderSubmitted, "a", nil))
	w.Write(New(KindFill, "a", nil))
	w.Write(New(KindError, "", map[string]string{"error": "x"}))
	w.Write(New(KindRun, "", map[string]string{"status": "completed"}))
	s, err := Summarize(File(dir, "2026-09-04"))
	if err != nil {
		t.Fatal(err)
	}
	if s.RunID != "2026-09-04" || s.Mode != "dry-run" || s.Events != 7 || s.Orders != 1 || s.Fills != 1 || s.Rejected != 1 || s.Errors != 1 || s.Status != "completed" {
		t.Errorf("summary = %+v", s)
	}
}
