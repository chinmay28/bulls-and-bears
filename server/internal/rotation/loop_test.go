package rotation

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/chinmay28/bulls-and-bears/server/internal/strategy/xlksata"
)

type fired struct {
	phase xlksata.Phase
	at    time.Time
}

// clock drives the loop through a scripted stretch of time, advancing on
// every sleep, and stops it once the horizon is reached.
type clock struct {
	now    time.Time
	stopAt time.Time
	cancel context.CancelFunc
}

func (c *clock) Now() time.Time { return c.now }

func (c *clock) Sleep(ctx context.Context, d time.Duration) bool {
	c.now = c.now.Add(d)
	if !c.now.Before(c.stopAt) {
		c.cancel()
		return false
	}
	return ctx.Err() == nil
}

// runLoop drives a loop from start to stop and returns what fired.
func runLoop(t *testing.T, start, stop time.Time, edit func(*Loop)) []fired {
	t.Helper()
	ti, err := DefaultTimes()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &clock{now: start, stopAt: stop, cancel: cancel}

	var got []fired
	l := &Loop{
		Times: ti,
		Tick:  time.Minute,
		Now:   c.Now,
		Sleep: c.Sleep,
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Run: func(_ context.Context, p xlksata.Phase, at time.Time) error {
			got = append(got, fired{p, at})
			return nil
		},
	}
	if edit != nil {
		edit(l)
	}
	l.Serve(ctx)
	return got
}

func nyc(t *testing.T) *time.Location {
	t.Helper()
	ti, err := DefaultTimes()
	if err != nil {
		t.Fatal(err)
	}
	return ti.Loc
}

func count(fs []fired, p xlksata.Phase) int {
	n := 0
	for _, f := range fs {
		if f.phase == p {
			n++
		}
	}
	return n
}

// One ordinary Wednesday: entry once, review once, and manage cycles on the
// throttle in between.
func TestLoopFiresEachNamedPhaseOncePerDay(t *testing.T) {
	loc := nyc(t)
	start := time.Date(2026, 9, 9, 9, 0, 0, 0, loc)
	stop := time.Date(2026, 9, 9, 16, 30, 0, 0, loc)

	got := runLoop(t, start, stop, nil)

	if n := count(got, xlksata.PhaseEntry); n != 1 {
		t.Errorf("entry fired %d times, want 1", n)
	}
	if n := count(got, xlksata.PhaseReview); n != 1 {
		t.Errorf("review fired %d times, want 1", n)
	}
	if n := count(got, xlksata.PhaseManage); n < 10 {
		t.Errorf("manage fired %d times, want the session's worth", n)
	}

	for _, f := range got {
		local := f.at.In(loc)
		switch f.phase {
		case xlksata.PhaseEntry:
			if local.Hour() != 10 || local.Minute() != 12 {
				t.Errorf("entry fired at %s, want 10:12 ET (07:12 PT)", local.Format("15:04"))
			}
		case xlksata.PhaseReview:
			if local.Hour() != 15 || local.Minute() != 7 {
				t.Errorf("review fired at %s, want 15:07 ET (12:07 PT)", local.Format("15:04"))
			}
		}
	}
}

func TestLoopThrottlesManageCycles(t *testing.T) {
	loc := nyc(t)
	start := time.Date(2026, 9, 9, 11, 0, 0, 0, loc)
	stop := time.Date(2026, 9, 9, 13, 0, 0, 0, loc)

	got := runLoop(t, start, stop, func(l *Loop) { l.ManageEvery = 30 * time.Minute })

	// Two hours at one every thirty minutes: the first is immediate.
	if n := count(got, xlksata.PhaseManage); n < 4 || n > 5 {
		t.Fatalf("manage fired %d times, want about 5", n)
	}
	var last time.Time
	for _, f := range got {
		if !last.IsZero() && f.at.Sub(last) < 30*time.Minute {
			t.Fatalf("two manage cycles %s apart", f.at.Sub(last))
		}
		last = f.at
	}
}

// A day the exchange is shut has no phases at all.
func TestLoopIsSilentOnAClosedDay(t *testing.T) {
	loc := nyc(t)
	cases := map[string]time.Time{
		"a Saturday":    time.Date(2026, 9, 12, 9, 0, 0, 0, loc),
		"a Sunday":      time.Date(2026, 9, 13, 9, 0, 0, 0, loc),
		"Christmas Day": time.Date(2026, 12, 25, 9, 0, 0, 0, loc),
	}
	for name, start := range cases {
		t.Run(name, func(t *testing.T) {
			if got := runLoop(t, start, start.Add(8*time.Hour), nil); len(got) != 0 {
				t.Fatalf("fired %d times on a closed day", len(got))
			}
		})
	}
}

// Outside the session's hours on an open day there is likewise no phase.
func TestLoopIsSilentOutsideSessionHours(t *testing.T) {
	loc := nyc(t)
	start := time.Date(2026, 9, 9, 4, 0, 0, 0, loc)
	got := runLoop(t, start, time.Date(2026, 9, 9, 9, 25, 0, 0, loc), nil)
	if len(got) != 0 {
		t.Fatalf("fired %d times before the open", len(got))
	}
	start = time.Date(2026, 9, 9, 16, 5, 0, 0, loc)
	got = runLoop(t, start, time.Date(2026, 9, 9, 20, 0, 0, 0, loc), nil)
	if len(got) != 0 {
		t.Fatalf("fired %d times after the close", len(got))
	}
}

// An early close ends the session, and the calendar is what says so.
func TestLoopStopsAtAnEarlyClose(t *testing.T) {
	loc := nyc(t)
	// 2026-11-27 is the Friday after Thanksgiving: a 13:00 close.
	start := time.Date(2026, 11, 27, 12, 30, 0, 0, loc)
	stop := time.Date(2026, 11, 27, 15, 0, 0, 0, loc)

	got := runLoop(t, start, stop, func(l *Loop) { l.ManageEvery = time.Minute })
	if len(got) == 0 {
		t.Fatal("nothing fired before the early close")
	}
	for _, f := range got {
		if local := f.at.In(loc); local.Hour() >= 13 {
			t.Fatalf("fired at %s, past the 13:00 early close", local.Format("15:04"))
		}
	}
	// The 12:07 PT review is 15:07 ET, after this close, so it never runs.
	if n := count(got, xlksata.PhaseReview); n != 0 {
		t.Errorf("review fired %d times on a day that closed before it", n)
	}
}

func TestLoopHonoursTheHaltMarker(t *testing.T) {
	loc := nyc(t)
	got := runLoop(t,
		time.Date(2026, 9, 9, 10, 0, 0, 0, loc),
		time.Date(2026, 9, 9, 11, 0, 0, 0, loc),
		func(l *Loop) {
			l.Halted = func() (string, bool) { return "drawdown kill switch", true }
		})
	if len(got) != 0 {
		t.Fatalf("fired %d times while halted", len(got))
	}
}

// A halted phase is not marked done, so the day's entry still happens once
// the halt is lifted — as long as the phase window has not closed. The halt
// is read off the same clock the loop runs on, so this needs no goroutine.
func TestLoopResumesAfterAHaltIsLifted(t *testing.T) {
	loc := nyc(t)
	lift := time.Date(2026, 9, 9, 10, 14, 0, 0, loc)

	got := runLoop(t,
		time.Date(2026, 9, 9, 10, 0, 0, 0, loc),
		time.Date(2026, 9, 9, 11, 0, 0, 0, loc),
		func(l *Loop) {
			now := l.Now
			l.Halted = func() (string, bool) {
				if now().Before(lift) {
					return "paused", true
				}
				return "", false
			}
		})
	if n := count(got, xlksata.PhaseEntry); n != 1 {
		t.Fatalf("entry fired %d times, want 1 once the halt lifted", n)
	}
	for _, f := range got {
		if f.phase == xlksata.PhaseEntry && f.at.Before(lift) {
			t.Errorf("entry fired at %s, before the halt lifted", f.at.In(loc).Format("15:04"))
		}
	}
}

// A restart mid-session re-runs the day's phases. That is safe rather than
// wrong: Cycle rereads the book every time and the strategy refuses a second
// lot, so the repeat decides to do nothing.
func TestLoopRestartRerunsTheDaysPhases(t *testing.T) {
	loc := nyc(t)
	first := runLoop(t, time.Date(2026, 9, 9, 10, 0, 0, 0, loc), time.Date(2026, 9, 9, 10, 30, 0, 0, loc), nil)
	if count(first, xlksata.PhaseEntry) != 1 {
		t.Fatalf("entry fired %d times", count(first, xlksata.PhaseEntry))
	}
	// A fresh Loop over the same window is a restart.
	second := runLoop(t, time.Date(2026, 9, 9, 10, 13, 0, 0, loc), time.Date(2026, 9, 9, 10, 30, 0, 0, loc), nil)
	if count(second, xlksata.PhaseEntry) != 1 {
		t.Errorf("a restart inside the entry window should still run it, got %d",
			count(second, xlksata.PhaseEntry))
	}
}
