package review

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The deck's class calendar (#242, part of #238), stored under decks.preset.calendar as
// {"calendar":{"startDate":"2026-09-08","weekdays":[2,4],"skip":["2026-11-26"]}} -- the third of
// the three release-pacing layers. Layer 1 (which notes are lesson 3) is notes.release_day; layer
// 2 (lesson 3 is the 3rd meeting) is identity and stores nothing; this is layer 3 (meeting 5 is
// Thu Oct 2), so the teacher never converts "week 6, Thursday" into a date by hand.
//
// The calendar's PRESENCE is the switch -- there is no separate enable flag, and so no state where
// a calendar is configured but inert.
const (
	// ReleaseGateOff is the current_class_day the study queries take for a deck with no calendar:
	// a class day past every lesson that can exist, so `release_day <= current_class_day` admits
	// everything and the gate needs no second branch. The same "sentinel beyond every real value"
	// trick, and the same constant, that reviews.sql already uses for import_due_position (#82).
	ReleaseGateOff int32 = math.MaxInt32

	// MaxReleaseDay bounds notes.release_day, and must match migration 00020's CHECK -- this is
	// the friendly-error copy of that constraint, not the constraint itself. It also bounds how
	// far the deck page's calendar view enumerates meetings, so a mistyped lesson number can't
	// turn that into a runaway walk.
	MaxReleaseDay int32 = 999

	// MaxCalendarSkipDates bounds the skip list -- generous for a term's holidays, small enough
	// that the stored preset stays a settings blob.
	MaxCalendarSkipDates = 60

	// CalendarDateLayout is the wire format for startDate and skip entries: a local calendar
	// date, never an instant. Timezone is deliberately absent -- the gate resolves against each
	// student's own study day (GetStudyDayWindow), not a timezone stored on the deck.
	CalendarDateLayout = "2006-01-02"
)

// Calendar is a deck's parsed class calendar. The zero value means "no calendar", which is what
// every deck that never uses release pacing has.
//
// StartDate and Skip are dates, held at UTC midnight rather than in any real timezone: all the
// arithmetic here is civil-date arithmetic, and normalising to UTC is what keeps a DST transition
// from turning a day difference into 23 or 25 hours. Weekdays are ISO 8601 numbers (Mon=1 ..
// Sun=7), sorted and deduplicated.
type Calendar struct {
	StartDate time.Time
	Weekdays  []int32
	Skip      []time.Time
}

// Configured reports whether this deck is paced at all. A calendar with no meeting weekdays is
// deliberately NOT configured: it could never advance past class day 0, so it would lock every
// assigned note forever -- the opposite of the permissive failure mode release_day 0 gives.
func (c Calendar) Configured() bool {
	return !c.StartDate.IsZero() && len(c.Weekdays) > 0
}

// meets reports whether iso (Mon=1 .. Sun=7) is one of the calendar's meeting weekdays.
func (c Calendar) meets(iso int32) bool { return slices.Contains(c.Weekdays, iso) }

// skipped reports whether d (a UTC-midnight date) is on the calendar's skip list.
func (c Calendar) skipped(d time.Time) bool { return slices.ContainsFunc(c.Skip, d.Equal) }

// SkipDates is the skip list in wire form, shared by JSON and the settings form's textarea so
// both render dates through the one layout this file owns.
func (c Calendar) SkipDates() []string {
	dates := make([]string, 0, len(c.Skip))
	for _, s := range c.Skip {
		dates = append(dates, s.Format(CalendarDateLayout))
	}
	return dates
}

// isoWeekday converts Go's Sunday=0 weekday to ISO 8601's Monday=1 .. Sunday=7.
func isoWeekday(t time.Time) int32 {
	if t.Weekday() == time.Sunday {
		return 7
	}
	return int32(t.Weekday())
}

// calendarWire is decks.preset.calendar on the wire, and the one input shape every calendar is
// built from -- the stored blob and the settings form both become one of these before
// calendarFromWire applies the rules, so the read and write paths cannot drift.
type calendarWire struct {
	StartDate string   `json:"startDate"`
	Weekdays  []int32  `json:"weekdays"`
	Skip      []string `json:"skip"`
}

// ErrInvalidCalendar is the single failure mode: the deck settings form answers every violation
// the same way (400, nothing written), so there is nothing for the error to carry.
var ErrInvalidCalendar = errors.New("review: invalid class calendar")

// calendarFromWire holds every rule about what a calendar may be. Its two callers differ only in
// how they react to a violation -- ParseCalendar degrades, NewCalendar reports.
func calendarFromWire(w calendarWire) (Calendar, error) {
	start, err := time.Parse(CalendarDateLayout, strings.TrimSpace(w.StartDate))
	if err != nil {
		return Calendar{}, ErrInvalidCalendar
	}
	cal := Calendar{StartDate: start}
	for _, day := range w.Weekdays {
		if day < 1 || day > 7 {
			return Calendar{}, ErrInvalidCalendar
		}
		if !cal.meets(day) {
			cal.Weekdays = append(cal.Weekdays, day)
		}
	}
	if len(cal.Weekdays) == 0 {
		return Calendar{}, ErrInvalidCalendar
	}
	sort.Slice(cal.Weekdays, func(i, j int) bool { return cal.Weekdays[i] < cal.Weekdays[j] })
	for _, raw := range w.Skip {
		d, err := time.Parse(CalendarDateLayout, raw)
		if err != nil {
			return Calendar{}, ErrInvalidCalendar
		}
		if !cal.skipped(d) {
			cal.Skip = append(cal.Skip, d)
		}
		if len(cal.Skip) > MaxCalendarSkipDates {
			return Calendar{}, ErrInvalidCalendar
		}
	}
	sort.Slice(cal.Skip, func(i, j int) bool { return cal.Skip[i].Before(cal.Skip[j]) })
	return cal, nil
}

// ParseCalendar reads decks.preset. Absent, malformed, or out of range -> the zero Calendar (no
// calendar, no gate) -- the same degrade-to-default rule as every other preset field, and for the
// same reason: Postgres has no safe cast, so a bad value must not 500 every study fetch. Malformed
// is all-or-nothing rather than per-field: half a calendar would silently shift every meeting date
// by however many holidays failed to parse, which is worse than not pacing the deck at all.
func ParseCalendar(preset []byte) Calendar {
	p, ok := parseDeckPreset(preset)
	if !ok || p.Calendar == nil {
		return Calendar{}
	}
	cal, err := calendarFromWire(*p.Calendar)
	if err != nil {
		return Calendar{}
	}
	return cal
}

// NewCalendar builds a Calendar from the deck settings form's raw strings -- the write-side
// counterpart to ParseCalendar. weekdays are the checked ISO day numbers; skip is free text, one
// date per line or separated by commas or spaces. Strict where ParseCalendar degrades: there is a
// human at the form to correct it.
func NewCalendar(startDate string, weekdays []string, skip string) (Calendar, error) {
	w := calendarWire{StartDate: startDate}
	for _, raw := range weekdays {
		day, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return Calendar{}, ErrInvalidCalendar
		}
		w.Weekdays = append(w.Weekdays, int32(day))
	}
	w.Skip = strings.FieldsFunc(skip, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	return calendarFromWire(w)
}

// JSON is the calendar as it goes back into decks.preset.calendar -- the inverse of ParseCalendar,
// written by the deck settings form (POST /decks/{id}/edit).
func (c Calendar) JSON() ([]byte, error) {
	return json.Marshal(calendarWire{
		StartDate: c.StartDate.Format(CalendarDateLayout),
		Weekdays:  c.Weekdays,
		Skip:      c.SkipDates(),
	})
}

// CurrentClassDay counts meeting weekdays in [StartDate, localDate], minus skipped dates. 0 when
// localDate precedes StartDate, or no calendar is configured.
//
// localDate is the student's own study day (GetStudyDayWindow's study_day_local_date), so "lesson
// 4 opens on Thursday" means their Thursday. A day that is not a meeting day holds the count at
// the last meeting's number, which is exactly what cumulative unlocking requires: once a lesson
// unlocks it stays unlocked, and a student who falls behind gets a backlog rather than a closed
// door.
func CurrentClassDay(cal Calendar, localDate time.Time) int32 {
	if !cal.Configured() {
		return 0
	}
	d := toDate(localDate)
	if d.Before(cal.StartDate) {
		return 0
	}
	// Whole weeks contribute one meeting per configured weekday each, so only the ragged tail
	// needs walking -- a term studied years after its start date is still O(1) plus at most six
	// days plus the skip list, not one iteration per elapsed day.
	total := int(d.Sub(cal.StartDate)/(24*time.Hour)) + 1 // inclusive of both ends
	count := (total / 7) * len(cal.Weekdays)
	tailStart := cal.StartDate.AddDate(0, 0, (total/7)*7)
	for i := 0; i < total%7; i++ {
		if cal.meets(isoWeekday(tailStart.AddDate(0, 0, i))) {
			count++
		}
	}
	// Skips are deduplicated when the calendar is built, and only a skip that falls on a meeting
	// weekday inside the window was ever counted above, so this can't drive the count below zero.
	for _, s := range cal.Skip {
		if !s.Before(cal.StartDate) && !s.After(d) && cal.meets(isoWeekday(s)) {
			count--
		}
	}
	return int32(count)
}

// GateDay is what the study queries take as current_class_day: this deck's resolved class day, or
// ReleaseGateOff when it has no calendar, so an unpaced deck admits every lesson.
func (c Calendar) GateDay(localDate time.Time) int32 {
	if !c.Configured() {
		return ReleaseGateOff
	}
	return CurrentClassDay(c, localDate)
}

// ReleaseGateDay is GateDay straight off a deck's preset, matching the shape of every other preset
// reader here (NewPerDay, RevPerDay, ParseRevOrder, ParsePriority, DueLookAheadMinutes). Callers
// that also need the Calendar itself -- the deck page's calendar view -- parse it once and use
// GateDay instead.
func ReleaseGateDay(preset []byte, localDate time.Time) int32 {
	return ParseCalendar(preset).GateDay(localDate)
}

// MeetingDates returns the dates of class days 1..n, in order -- the deck page's calendar view
// (#242), which is where a teacher checks that "lesson 5" really is the Thursday she has in mind.
// n is clamped to MaxReleaseDay; an unconfigured calendar has no meetings.
func MeetingDates(cal Calendar, n int32) []time.Time {
	if !cal.Configured() || n <= 0 {
		return nil
	}
	n = min(n, MaxReleaseDay)
	dates := make([]time.Time, 0, n)
	// Every 7-day span holds at least one meeting weekday, so walking 7*(n+len(skip)) days from
	// the start date always reaches n meetings -- a hard bound, so a pathological skip list can
	// shorten this walk but never make it unbounded.
	limit := 7 * (int(n) + len(cal.Skip))
	for i := 0; i < limit && int32(len(dates)) < n; i++ {
		day := cal.StartDate.AddDate(0, 0, i)
		if cal.meets(isoWeekday(day)) && !cal.skipped(day) {
			dates = append(dates, day)
		}
	}
	return dates
}

// MeetingDate returns the date class day n falls on, and whether the calendar has an nth meeting
// at all -- the inverse of CurrentClassDay, and what turns a locked lesson number back into the
// "Lesson 4 unlocks Thursday, 9 October" line a student sees (#243). n above MaxReleaseDay is
// unresolvable rather than clamped: a clamped answer would name the wrong date, and no note can
// carry a lesson number that high anyway (migration 00020's CHECK).
func MeetingDate(cal Calendar, n int32) (time.Time, bool) {
	if n <= 0 || n > MaxReleaseDay {
		return time.Time{}, false
	}
	dates := MeetingDates(cal, n)
	if int32(len(dates)) < n {
		return time.Time{}, false
	}
	return dates[n-1], true
}

// toDate normalises t to UTC midnight so every comparison here is a civil-date comparison. A date
// read back from Postgres already arrives this way; a time.Time from anywhere else may not.
func toDate(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
