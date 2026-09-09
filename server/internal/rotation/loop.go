package rotation

import (
	"context"
	"log/slog"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/sched"
	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/xlksata"
)

// Loop fires the rotation's cycles through a trading session.
//
// It is a separate loop from sched.Loop rather than a second fire time on
// it, because the two answer different questions. That one asks "when does
// today close?" and runs once against it; this one asks "what is the market
// doing right now?" several times a day. Both read the same calendar, so a
// holiday or an early close is honoured identically.
//
// The two named phases fire once each per trading day. Manage cycles fire on
// a throttle between them, which is what lets a recovery write a call, take
// its target, or sweep proceeds into SATA without waiting for tomorrow.
type Loop struct {
	Times Times
	// Tick is how often the clock is consulted; a phase window is wider
	// than this, so a tick can never step over one.
	Tick time.Duration
	// ManageEvery throttles the unnamed cycles. Zero runs one per tick.
	ManageEvery time.Duration
	// Run does one cycle.
	Run func(ctx context.Context, phase xlksata.Phase, now time.Time) error
	// Halted skips a cycle and says why. Checked at the moment of the
	// cycle, not when it was scheduled, so the kill switch works mid-day.
	Halted func() (reason string, halted bool)
	Now    func() time.Time
	Sleep  func(ctx context.Context, d time.Duration) bool
	Log    *slog.Logger
}

const (
	defaultTick        = time.Minute
	defaultManageEvery = 15 * time.Minute
)

// Serve blocks until ctx is done.
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
	tick := l.Tick
	if tick <= 0 {
		tick = defaultTick
	}
	manageEvery := l.ManageEvery
	if manageEvery <= 0 {
		manageEvery = defaultManageEvery
	}

	// done[runID+phase] keeps the named phases to one firing a day; a
	// restart mid-session re-runs them, which is safe because Cycle reads
	// the book every time and the strategy refuses a second lot.
	done := map[string]bool{}
	var lastManage time.Time

	for {
		t := now()
		phase, armed, session := l.phaseNow(t)
		switch {
		case !armed:
			// Outside a session. Nothing to do until the next one opens.

		case phase == xlksata.PhaseManage && !lastManage.IsZero() && t.Sub(lastManage) < manageEvery:
			// Throttled.

		case phase != xlksata.PhaseManage && done[session+phase.String()]:
			// Already fired today.

		default:
			if reason, halted := l.halted(); halted {
				log.Warn("rotation: halted", "phase", phase.String(), "reason", reason)
			} else {
				if err := l.Run(ctx, phase, t); err != nil {
					// Logged, not retried: the same inputs a minute later
					// give the same answer, and a halt is a halt.
					log.Error("rotation: cycle failed", "phase", phase.String(), "err", err)
				}
				if phase == xlksata.PhaseManage {
					lastManage = t
				} else {
					done[session+phase.String()] = true
				}
			}
		}

		if !sleep(ctx, tick) {
			return
		}
	}
}

// phaseNow is the phase at t, and the run id of the session it belongs to.
// A day the exchange is shut has no phase at all.
func (l *Loop) phaseNow(t time.Time) (xlksata.Phase, bool, string) {
	s, ok := sched.SessionOn(t.In(l.Times.Loc))
	if !ok {
		return 0, false, ""
	}
	phase, armed := l.Times.Phase(t)
	if !armed {
		return 0, false, s.RunID()
	}
	// An early close ends the session early; the calendar knows when. The
	// close is the moment trading stops, so it is exclusive: 13:00:00 on a
	// half day is already shut.
	if !t.Before(s.Close) {
		return 0, false, s.RunID()
	}
	return phase, true, s.RunID()
}

func (l *Loop) halted() (string, bool) {
	if l.Halted == nil {
		return "", false
	}
	return l.Halted()
}

func realSleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
