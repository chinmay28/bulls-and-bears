package sched

import (
	"testing"
	"time"
)

func date(y int, m time.Month, d int) time.Time { return ny(y, m, d) }

func TestHolidays(t *testing.T) {
	cases := []struct {
		day  time.Time
		open bool
		why  string
	}{
		{date(2026, 1, 1), false, "New Year's Day"},
		{date(2026, 1, 2), true, "day after New Year's"},
		{date(2026, 1, 19), false, "MLK Day, third Monday of January"},
		{date(2026, 2, 16), false, "Presidents' Day"},
		{date(2026, 4, 3), false, "Good Friday 2026"},
		{date(2026, 5, 25), false, "Memorial Day, last Monday of May"},
		{date(2026, 6, 19), false, "Juneteenth"},
		{date(2026, 7, 3), false, "Independence Day observed: July 4 2026 is a Saturday"},
		{date(2026, 7, 4), false, "a Saturday"},
		{date(2026, 9, 7), false, "Labor Day"},
		{date(2026, 11, 26), false, "Thanksgiving"},
		{date(2026, 11, 27), true, "day after Thanksgiving is an early close, still open"},
		{date(2026, 12, 25), false, "Christmas"},
		{date(2027, 1, 1), false, "New Year's Day 2027, a Friday"},
		{date(2022, 1, 1), false, "a Saturday"},
		{date(2021, 12, 31), true, "New Year's 2022 on a Saturday is not observed on the Friday"},
		{date(2023, 1, 2), false, "New Year's 2023 on a Sunday is observed Monday"},
		{date(2023, 4, 7), false, "Good Friday 2023"},
		{date(2024, 3, 29), false, "Good Friday 2024"},
		{date(2025, 4, 18), false, "Good Friday 2025"},
		{date(2025, 12, 24), true, "Christmas Eve is open, early"},
		{date(2026, 9, 4), true, "an ordinary Friday"},
	}
	for _, c := range cases {
		_, open := SessionOn(c.day)
		if open != c.open {
			t.Errorf("%s (%s): open = %v, want %v", c.day.Format("2006-01-02 Mon"), c.why, open, c.open)
		}
	}
}

func TestEarlyCloses(t *testing.T) {
	cases := []struct {
		day   time.Time
		early bool
		why   string
	}{
		{date(2026, 11, 27), true, "day after Thanksgiving"},
		{date(2025, 12, 24), true, "Christmas Eve on a Wednesday"},
		{date(2026, 12, 24), true, "Christmas Eve on a Thursday"},
		{date(2025, 7, 3), true, "July 3 with July 4 on a Friday"},
		{date(2026, 9, 4), false, "an ordinary day"},
	}
	for _, c := range cases {
		s, ok := SessionOn(c.day)
		if !ok {
			t.Fatalf("%s: expected a session", c.day)
		}
		if s.Early != c.early {
			t.Errorf("%s (%s): early = %v, want %v", c.day.Format("2006-01-02"), c.why, s.Early, c.early)
		}
		wantHour := 16
		if c.early {
			wantHour = 13
		}
		if s.Close.Hour() != wantHour || s.Close.Location() != NewYork {
			t.Errorf("%s: close = %v", c.day.Format("2006-01-02"), s.Close)
		}
	}
}

func TestNextClose(t *testing.T) {
	cases := []struct {
		now  time.Time
		want string
		why  string
	}{
		{time.Date(2026, 9, 4, 10, 0, 0, 0, NewYork), "2026-09-04", "morning of a trading day"},
		{time.Date(2026, 9, 4, 16, 0, 0, 0, NewYork), "2026-09-08", "at the close on Friday, Monday is Labor Day"},
		{time.Date(2026, 9, 4, 15, 59, 59, 0, NewYork), "2026-09-04", "a second before the close"},
		{time.Date(2026, 9, 5, 12, 0, 0, 0, NewYork), "2026-09-08", "Saturday"},
		{time.Date(2026, 11, 27, 13, 30, 0, 0, NewYork), "2026-11-30", "after an early close"},
		{time.Date(2026, 9, 4, 19, 0, 0, 0, time.UTC), "2026-09-04", "UTC input, 15:00 New York"},
	}
	for _, c := range cases {
		s, err := NextClose(c.now)
		if err != nil {
			t.Fatalf("%s: %v", c.why, err)
		}
		if s.RunID() != c.want {
			t.Errorf("%s: next = %s, want %s", c.why, s.RunID(), c.want)
		}
	}
}

func TestRunIDIsTheTradingDate(t *testing.T) {
	s, _ := SessionOn(date(2026, 9, 4))
	if s.RunID() != "2026-09-04" {
		t.Errorf("RunID = %q", s.RunID())
	}
}
