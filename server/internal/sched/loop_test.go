package sched

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// fakeClock advances only when the loop sleeps, so a test runs a whole
// trading day in microseconds.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) bool {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return ctx.Err() == nil
}

func runLoop(t *testing.T, start time.Time, done func(string) bool, halted func() (string, bool)) []string {
	t.Helper()
	clock := &fakeClock{now: start}
	var mu sync.Mutex
	var ran []string
	ctx, cancel := context.WithCancel(context.Background())
	l := &Loop{
		Offset: 10 * time.Minute,
		Done:   done,
		Now:    clock.Now,
		Sleep:  clock.Sleep,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Halted: halted,
		Run: func(ctx context.Context, s Session) error {
			mu.Lock()
			ran = append(ran, s.RunID())
			mu.Unlock()
			cancel()
			return nil
		},
	}
	finished := make(chan struct{})
	go func() {
		l.Serve(ctx)
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("loop did not finish")
	}
	mu.Lock()
	defer mu.Unlock()
	return ran
}

func TestLoopRunsTenMinutesBeforeTheClose(t *testing.T) {
	start := time.Date(2026, 9, 4, 15, 0, 0, 0, NewYork)
	ran := runLoop(t, start, nil, nil)
	if len(ran) != 1 || ran[0] != "2026-09-04" {
		t.Fatalf("ran = %v", ran)
	}
}

func TestLoopSkipsADayAlreadyDone(t *testing.T) {
	start := time.Date(2026, 9, 4, 15, 0, 0, 0, NewYork)
	// Friday is done; Monday is Labor Day; Tuesday is next.
	ran := runLoop(t, start, func(id string) bool { return id == "2026-09-04" }, nil)
	if len(ran) != 1 || ran[0] != "2026-09-08" {
		t.Fatalf("ran = %v, want Tuesday", ran)
	}
}

func TestLoopRunsLateIfRestartedAfterTheRunTime(t *testing.T) {
	start := time.Date(2026, 9, 4, 15, 55, 0, 0, NewYork)
	ran := runLoop(t, start, nil, nil)
	if len(ran) != 1 || ran[0] != "2026-09-04" {
		t.Fatalf("ran = %v, want today's run, late", ran)
	}
}

func TestLoopHonoursTheHalt(t *testing.T) {
	start := time.Date(2026, 9, 4, 15, 0, 0, 0, NewYork)
	halted := true
	ran := runLoop(t, start, nil, func() (string, bool) {
		if halted {
			halted = false // resumed over the weekend
			return "test", true
		}
		return "", false
	})
	if len(ran) != 1 || ran[0] != "2026-09-08" {
		t.Fatalf("ran = %v, want Friday skipped and Tuesday run", ran)
	}
}
