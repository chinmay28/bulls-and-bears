// Package sched knows when the market is open and runs the daily cycle once
// per trading day, a little before the close.
//
// It owns the NYSE calendar — full holidays and the early closes — and the
// loop that waits for T minus a configurable offset and fires. It does not
// know what a run does: the caller hands it a function and a way to ask
// whether a day's run already happened, which is what makes a restart in the
// afternoon safe. The run id is the trading date, so two processes, or one
// process twice, cannot run the same day twice.
package sched

import (
	"fmt"
	"time"
)

// NewYork is the exchange's clock. Loaded once; a machine without tzdata
// cannot know when the close is, and refuses to guess.
var NewYork = mustLoad("America/New_York")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic("sched: " + name + ": " + err.Error())
	}
	return loc
}

// Session is one trading day: its date and when it closes, exchange time.
type Session struct {
	Date  time.Time // midnight, New York
	Close time.Time // 16:00, or 13:00 on an early-close day
	Early bool
}

// RunID is the run id for a session: the trading date, YYYY-MM-DD.
func (s Session) RunID() string { return s.Date.Format("2006-01-02") }

// SessionOn reports the session on a calendar date, or false on a weekend or
// holiday. The date's clock and zone are ignored; only Y-M-D count.
func SessionOn(date time.Time) (Session, bool) {
	y, m, d := date.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, NewYork)
	if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday || isHoliday(day) {
		return Session{}, false
	}
	closeHour := 16
	early := isEarlyClose(day)
	if early {
		closeHour = 13
	}
	return Session{
		Date:  day,
		Close: time.Date(y, m, d, closeHour, 0, 0, 0, NewYork),
		Early: early,
	}, true
}

// NextClose finds the first session whose close is after now, looking at most
// a fortnight ahead — long enough for any holiday run. It is the answer to
// "when does the next run happen?".
func NextClose(now time.Time) (Session, error) {
	day := now.In(NewYork)
	for i := 0; i < 14; i++ {
		if s, ok := SessionOn(day); ok && s.Close.After(now) {
			return s, nil
		}
		day = day.AddDate(0, 0, 1)
	}
	return Session{}, fmt.Errorf("sched: no trading session within 14 days of %s", now.Format(time.RFC3339))
}

// isHoliday applies the NYSE holiday rules. A holiday falling on a Saturday
// is observed the Friday before; on a Sunday, the Monday after — except New
// Year's Day on a Saturday, which the exchange does not observe on the
// previous Friday (it would be December 31 of the old year).
func isHoliday(day time.Time) bool {
	y := day.Year()
	for _, h := range fixedHolidays(y) {
		if observed(h) == day {
			return true
		}
	}
	for _, h := range floatingHolidays(y) {
		if h.Equal(day) {
			return true
		}
	}
	return false
}

// fixedHolidays are the ones on a calendar date: New Year's, Juneteenth,
// Independence Day, Christmas.
func fixedHolidays(y int) []time.Time {
	return []time.Time{
		ny(y, time.January, 1),
		ny(y, time.June, 19),
		ny(y, time.July, 4),
		ny(y, time.December, 25),
	}
}

// floatingHolidays are the ones on a weekday of the month: MLK (3rd Monday
// of January), Presidents' Day (3rd Monday of February), Good Friday,
// Memorial Day (last Monday of May), Labor Day (1st Monday of September),
// Thanksgiving (4th Thursday of November).
func floatingHolidays(y int) []time.Time {
	return []time.Time{
		nthWeekday(y, time.January, time.Monday, 3),
		nthWeekday(y, time.February, time.Monday, 3),
		goodFriday(y),
		lastWeekday(y, time.May, time.Monday),
		nthWeekday(y, time.September, time.Monday, 1),
		nthWeekday(y, time.November, time.Thursday, 4),
	}
}

// isEarlyClose: 13:00 on the day after Thanksgiving, on Christmas Eve when it
// is a weekday, and on July 3 when Independence Day falls on a weekday.
func isEarlyClose(day time.Time) bool {
	y := day.Year()
	if day.Equal(nthWeekday(y, time.November, time.Thursday, 4).AddDate(0, 0, 1)) {
		return true
	}
	xmasEve := ny(y, time.December, 24)
	if day.Equal(xmasEve) && isWeekday(xmasEve) {
		return true
	}
	july3 := ny(y, time.July, 3)
	if day.Equal(july3) && isWeekday(july3) && isWeekday(ny(y, time.July, 4)) {
		return true
	}
	return false
}

func observed(h time.Time) time.Time {
	switch h.Weekday() {
	case time.Saturday:
		if h.Month() == time.January && h.Day() == 1 {
			return h // not observed
		}
		return h.AddDate(0, 0, -1)
	case time.Sunday:
		return h.AddDate(0, 0, 1)
	}
	return h
}

func isWeekday(t time.Time) bool {
	return t.Weekday() != time.Saturday && t.Weekday() != time.Sunday
}

func ny(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, NewYork)
}

func nthWeekday(y int, m time.Month, wd time.Weekday, n int) time.Time {
	first := ny(y, m, 1)
	offset := (int(wd) - int(first.Weekday()) + 7) % 7
	return first.AddDate(0, 0, offset+7*(n-1))
}

func lastWeekday(y int, m time.Month, wd time.Weekday) time.Time {
	last := ny(y, m+1, 1).AddDate(0, 0, -1)
	offset := (int(last.Weekday()) - int(wd) + 7) % 7
	return last.AddDate(0, 0, -offset)
}

// goodFriday is two days before Easter Sunday (Gregorian, by the anonymous
// algorithm).
func goodFriday(y int) time.Time {
	a := y % 19
	b := y / 100
	c := y % 100
	d := b / 4
	e := b % 4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i := c / 4
	k := c % 4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	month := time.Month((h + l - 7*m + 114) / 31)
	day := ((h + l - 7*m + 114) % 31) + 1
	return ny(y, month, day).AddDate(0, 0, -2)
}
