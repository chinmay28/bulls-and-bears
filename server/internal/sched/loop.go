package sched

import (
	"context"
	"log/slog"
	"time"
)

// Loop fires a run once per trading day at the close minus Offset.
type Loop struct {
	// Offset before the close to run at; the plan says ten minutes.
	Offset time.Duration
	// Done reports whether a run id has already completed, so a restart
	// after the run does not run it again. Usually answered from the
	// journal.
	Done func(runID string) bool
	// Run does the day's work. An error is logged, not retried: a run that
	// failed at 15:50 is not something to try again at 15:51 with the same
	// inputs, and the journal has the error.
	Run func(ctx context.Context, s Session) error
	// Now is the clock; nil means time.Now. Tests set it.
	Now func() time.Time
	// Sleep waits for d or until ctx ends, reporting false in the latter
	// case; nil means a real wait. Tests set it to advance their clock.
	Sleep func(ctx context.Context, d time.Duration) bool
	Log   *slog.Logger
	// Halted, when it returns true, skips the run and says so. The halt
	// marker is checked at the moment of the run, not when it was scheduled.
	Halted func() (reason string, halted bool)
}

// Serve blocks until ctx is done. If a session's run time has already passed
// today but the close has not, and the run is not done, it runs now: a
// process restarted at 15:55 still trades the day.
func (l *Loop) Serve(ctx context.Context) {
	now := time.Now
	if l.Now != nil {
		now = l.Now
	}
	sleep := realSleep
	if l.Sleep != nil {
		sleep = l.Sleep
	}
	log := l.Log
	if log == nil {
		log = slog.Default()
	}
	for {
		s, err := NextClose(now())
		if err != nil {
			log.Error("sched: no session", "err", err)
			return
		}
		at := s.Close.Add(-l.Offset)
		if l.Done != nil && l.Done(s.RunID()) {
			// Already ran today; wait for the next session.
			at = s.Close.Add(time.Minute)
			log.Debug("sched: run already done", "run_id", s.RunID(), "next_check", at)
			if !sleepUntil(ctx, at, now, sleep) {
				return
			}
			continue
		}
		log.Info("sched: next run", "run_id", s.RunID(), "at", at.Format(time.RFC3339), "early_close", s.Early)
		if !sleepUntil(ctx, at, now, sleep) {
			return
		}
		if l.Halted != nil {
			if reason, halted := l.Halted(); halted {
				log.Warn("sched: halted, skipping run", "run_id", s.RunID(), "reason", reason)
				// Wait out the close so the same session is not offered again.
				if !sleepUntil(ctx, s.Close.Add(time.Minute), now, sleep) {
					return
				}
				continue
			}
		}
		if err := l.Run(ctx, s); err != nil {
			log.Error("sched: run failed", "run_id", s.RunID(), "err", err)
		}
		// Whatever happened, the day is over for the scheduler.
		if !sleepUntil(ctx, s.Close.Add(time.Minute), now, sleep) {
			return
		}
	}
}

// sleepUntil waits for the wall clock to reach at, in short hops so a machine
// that sleeps through the night (a laptop lid) does not oversleep by the
// length of one long timer. False when ctx ended first.
func sleepUntil(ctx context.Context, at time.Time, now func() time.Time, sleep func(context.Context, time.Duration) bool) bool {
	for {
		d := at.Sub(now())
		if d <= 0 {
			return true
		}
		if d > time.Minute {
			d = time.Minute
		}
		if !sleep(ctx, d) {
			return false
		}
	}
}

func realSleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
