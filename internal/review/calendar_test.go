package review

import (
	"testing"
	"time"
)

// date is a UTC-midnight calendar date, the shape every value in this file is written in.
func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// termCalendar is the issue's own worked example (#242): Tue/Thu from Tue 8 Sep 2026, skipping
// Thanksgiving Thursday.
func termCalendar() Calendar {
	return Calendar{
		StartDate: date(2026, time.September, 8),
		Weekdays:  []int32{2, 4},
		Skip:      []time.Time{date(2026, time.November, 26)},
	}
}

// countMeetingsBruteForce is CurrentClassDay's definition walked one day at a time -- the
// independent implementation the closed-form (whole weeks + a ragged tail) is checked against.
func countMeetingsBruteForce(cal Calendar, localDate time.Time) int32 {
	var n int32
	for d := cal.StartDate; !d.After(localDate); d = d.AddDate(0, 0, 1) {
		if cal.meets(isoWeekday(d)) && !cal.skipped(d) {
			n++
		}
	}
	return n
}

func TestCurrentClassDay(t *testing.T) {
	// A calendar whose start date is not itself a meeting day: Mon 7 Sep, meeting Tue/Thu.
	offStart := Calendar{StartDate: date(2026, time.September, 7), Weekdays: []int32{2, 4}}
	// Thanksgiving week, so the skip lands inside a two-meeting window.
	skipWeek := Calendar{
		StartDate: date(2026, time.November, 24),
		Weekdays:  []int32{2, 4},
		Skip:      []time.Time{date(2026, time.November, 26)},
	}
	// Tue/Thu across the 8 Mar 2026 US DST transition.
	dstWeek := Calendar{StartDate: date(2026, time.March, 3), Weekdays: []int32{2, 4}}

	tests := []struct {
		name      string
		cal       Calendar
		localDate time.Time
		want      int32
	}{
		{"no calendar at all", Calendar{}, date(2026, time.September, 10), 0},
		{"day before the term starts", termCalendar(), date(2026, time.September, 7), 0},
		{"first meeting", termCalendar(), date(2026, time.September, 8), 1},
		{"non-meeting day holds at the last meeting", termCalendar(), date(2026, time.September, 9), 1},
		{"second meeting", termCalendar(), date(2026, time.September, 10), 2},
		{"weekend holds at the last meeting", termCalendar(), date(2026, time.September, 13), 2},
		{"third meeting, a week in", termCalendar(), date(2026, time.September, 15), 3},

		{"start date is not a meeting weekday", offStart, date(2026, time.September, 7), 0},
		{"first real meeting after an off-weekday start", offStart, date(2026, time.September, 8), 1},

		{"skipped meeting does not advance the count", skipWeek, date(2026, time.November, 26), 1},
		{"the meeting after a skip takes the skipped day's number", skipWeek, date(2026, time.December, 1), 2},

		{"across a DST transition", dstWeek, date(2026, time.March, 10), 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cal.CurrentClassDay(tt.localDate); got != tt.want {
				t.Errorf("CurrentClassDay = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestCurrentClassDay_MatchesBruteForce checks the whole-weeks-plus-tail arithmetic against a
// day-by-day count over a full term, for every meeting-weekday shape from one day a week to seven.
func TestCurrentClassDay_MatchesBruteForce(t *testing.T) {
	cals := []Calendar{
		termCalendar(),
		{StartDate: date(2026, time.September, 8), Weekdays: []int32{1}},
		{StartDate: date(2026, time.September, 8), Weekdays: []int32{1, 2, 3, 4, 5}},
		{StartDate: date(2026, time.September, 8), Weekdays: []int32{1, 2, 3, 4, 5, 6, 7}},
		{StartDate: date(2026, time.September, 13), Weekdays: []int32{7}}, // start on a Sunday
	}
	for i, cal := range cals {
		for day := 0; day < 200; day++ {
			d := cal.StartDate.AddDate(0, 0, day)
			want := countMeetingsBruteForce(cal, d)
			if got := cal.CurrentClassDay(d); got != want {
				t.Fatalf("calendar %d, %s: CurrentClassDay = %d, brute force = %d",
					i, d.Format(CalendarDateLayout), got, want)
			}
		}
	}
}

// TestCurrentClassDay_NormalisesLocalDate: a localDate carrying a wall-clock time in some other
// zone still resolves to its own calendar date, never the UTC instant's.
func TestCurrentClassDay_NormalisesLocalDate(t *testing.T) {
	eastern := time.FixedZone("EST", -5*60*60)
	// 8 Sep 2026 20:00 in EST is 9 Sep in UTC; the class day must still be Tuesday's, 1.
	if got := termCalendar().CurrentClassDay(time.Date(2026, time.September, 8, 20, 0, 0, 0, eastern)); got != 1 {
		t.Errorf("CurrentClassDay = %d, want 1", got)
	}
}

func TestGateDay(t *testing.T) {
	if got := (Calendar{}).GateDay(date(2026, time.September, 10)); got != ReleaseGateOff {
		t.Errorf("unconfigured calendar: GateDay = %d, want ReleaseGateOff (%d)", got, ReleaseGateOff)
	}
	// Before the term starts the deck is still paced -- class day 0, which locks every assigned
	// lesson rather than releasing everything.
	if got := termCalendar().GateDay(date(2026, time.September, 1)); got != 0 {
		t.Errorf("before start: GateDay = %d, want 0", got)
	}
	if got := termCalendar().GateDay(date(2026, time.September, 10)); got != 2 {
		t.Errorf("mid-term: GateDay = %d, want 2", got)
	}

	// ReleaseGateDay is the same answer read straight off a preset, the shape every other preset
	// reader in this package has.
	preset := []byte(`{"calendar":{"startDate":"2026-09-08","weekdays":[2,4]}}`)
	if got := ReleaseGateDay(preset, date(2026, time.September, 10)); got != 2 {
		t.Errorf("ReleaseGateDay(preset) = %d, want 2", got)
	}
	if got := ReleaseGateDay(nil, date(2026, time.September, 10)); got != ReleaseGateOff {
		t.Errorf("ReleaseGateDay(no preset) = %d, want ReleaseGateOff (%d)", got, ReleaseGateOff)
	}
}

// TestParseCalendar_Degrades: every malformed shape reads as "no calendar", which switches the
// gate off -- a study fetch must never fail on a bad preset (CLAUDE.md §9, docs/schema.md).
func TestParseCalendar_Degrades(t *testing.T) {
	tests := []struct {
		name   string
		preset string
	}{
		{"nil preset", ""},
		{"empty preset", `{}`},
		{"malformed json", `{"calendar":`},
		{"calendar is not an object", `{"calendar": 7}`},
		{"missing start date", `{"calendar":{"weekdays":[2,4]}}`},
		{"unparseable start date", `{"calendar":{"startDate":"8 Sep 2026","weekdays":[2,4]}}`},
		{"no meeting weekdays", `{"calendar":{"startDate":"2026-09-08","weekdays":[]}}`},
		{"weekday out of range", `{"calendar":{"startDate":"2026-09-08","weekdays":[0,4]}}`},
		{"weekday above Sunday", `{"calendar":{"startDate":"2026-09-08","weekdays":[2,8]}}`},
		{"unparseable skip date", `{"calendar":{"startDate":"2026-09-08","weekdays":[2],"skip":["nope"]}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cal := ParseCalendar([]byte(tt.preset))
			if cal.Configured() {
				t.Fatalf("ParseCalendar(%q) reported a configured calendar, want none", tt.preset)
			}
			if got := cal.GateDay(date(2026, time.September, 10)); got != ReleaseGateOff {
				t.Errorf("GateDay = %d, want ReleaseGateOff (%d)", got, ReleaseGateOff)
			}
		})
	}
}

func TestParseCalendar_RoundTrip(t *testing.T) {
	// Deliberately unsorted and duplicated on the way in: ParseCalendar normalises both.
	preset := []byte(`{"new":{"perDay":5},"calendar":{"startDate":"2026-09-08","weekdays":[4,2,4],"skip":["2026-11-26","2026-11-26"]}}`)
	cal := ParseCalendar(preset)
	if !cal.Configured() {
		t.Fatal("ParseCalendar reported no calendar")
	}
	want := termCalendar()
	if !cal.StartDate.Equal(want.StartDate) {
		t.Errorf("StartDate = %s, want %s", cal.StartDate, want.StartDate)
	}
	if len(cal.Weekdays) != 2 || cal.Weekdays[0] != 2 || cal.Weekdays[1] != 4 {
		t.Errorf("Weekdays = %v, want [2 4]", cal.Weekdays)
	}
	if len(cal.Skip) != 1 || !cal.Skip[0].Equal(want.Skip[0]) {
		t.Errorf("Skip = %v, want %v", cal.Skip, want.Skip)
	}
	// The other preset fields still parse off the same blob.
	if got := NewPerDay(preset); got != 5 {
		t.Errorf("NewPerDay = %d, want 5", got)
	}

	encoded, err := cal.JSON()
	if err != nil {
		t.Fatalf("Calendar.JSON: %v", err)
	}
	reparsed := ParseCalendar([]byte(`{"calendar":` + string(encoded) + `}`))
	if got, want := reparsed.CurrentClassDay(date(2026, time.December, 1)), cal.CurrentClassDay(date(2026, time.December, 1)); got != want {
		t.Errorf("round-tripped calendar resolves to class day %d, want %d", got, want)
	}
}

func TestNewCalendar(t *testing.T) {
	cal, err := NewCalendar("2026-09-08", []string{"4", "2"}, "2026-11-26\n2026-11-26, 2026-11-27")
	if err != nil {
		t.Fatalf("NewCalendar: %v", err)
	}
	if len(cal.Weekdays) != 2 || cal.Weekdays[0] != 2 {
		t.Errorf("Weekdays = %v, want sorted [2 4]", cal.Weekdays)
	}
	if len(cal.Skip) != 2 {
		t.Errorf("Skip = %v, want the duplicate collapsed and both distinct dates kept", cal.Skip)
	}

	bad := []struct {
		name      string
		startDate string
		weekdays  []string
		skip      string
	}{
		{"unparseable start date", "8 Sep 2026", []string{"2"}, ""},
		{"no meeting weekdays", "2026-09-08", nil, ""},
		{"weekday out of range", "2026-09-08", []string{"8"}, ""},
		{"non-numeric weekday", "2026-09-08", []string{"Tue"}, ""},
		{"unparseable skip date", "2026-09-08", []string{"2"}, "Thanksgiving"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewCalendar(tt.startDate, tt.weekdays, tt.skip); err == nil {
				t.Error("NewCalendar accepted an invalid calendar, want ErrInvalidCalendar")
			}
		})
	}

	t.Run("too many skip dates", func(t *testing.T) {
		var skip string
		for i := 0; i <= MaxCalendarSkipDates; i++ {
			skip += date(2026, time.September, 8).AddDate(0, 0, i).Format(CalendarDateLayout) + "\n"
		}
		if _, err := NewCalendar("2026-09-08", []string{"2"}, skip); err == nil {
			t.Error("NewCalendar accepted more than MaxCalendarSkipDates skip dates")
		}
	})
}

// TestMeetingDate_IsTheInverseOfCurrentClassDay is the property the student-facing unlock line
// (#243) rests on: the date named for lesson N is a date on which the gate has actually reached N.
// Checked the day before too -- the line must not name a date the lesson is already open on.
func TestMeetingDate_IsTheInverseOfCurrentClassDay(t *testing.T) {
	cals := []Calendar{
		termCalendar(), // Tue/Thu, and 40 lessons from 8 Sep reach its Thanksgiving skip date
		{StartDate: date(2026, time.September, 8), Weekdays: []int32{1}},
		{StartDate: date(2026, time.September, 8), Weekdays: []int32{1, 2, 3, 4, 5}},
		// Tue/Thu across the 8 Mar 2026 US DST transition.
		{StartDate: date(2026, time.March, 3), Weekdays: []int32{2, 4}},
	}
	for i, cal := range cals {
		for lesson := int32(1); lesson <= 40; lesson++ {
			d, ok := cal.MeetingDate(lesson)
			if !ok {
				t.Fatalf("calendar %d: no date for lesson %d", i, lesson)
			}
			if got := cal.CurrentClassDay(d); got != lesson {
				t.Errorf("calendar %d: lesson %d unlocks %s, but that date resolves to class day %d",
					i, lesson, d.Format(CalendarDateLayout), got)
			}
			if got := cal.CurrentClassDay(d.AddDate(0, 0, -1)); got >= lesson {
				t.Errorf("calendar %d: lesson %d was already open on %s, the day before its unlock date",
					i, lesson, d.AddDate(0, 0, -1).Format(CalendarDateLayout))
			}
		}
	}
}

func TestMeetingDate_Unresolvable(t *testing.T) {
	tests := []struct {
		name   string
		cal    Calendar
		lesson int32
	}{
		{"no calendar", Calendar{}, 3},
		// 0 is "no lesson assigned", which is available immediately and so has no unlock date.
		{"lesson 0", termCalendar(), 0},
		{"negative lesson", termCalendar(), -1},
		{"above MaxReleaseDay", termCalendar(), MaxReleaseDay + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := tt.cal.MeetingDate(tt.lesson); ok {
				t.Errorf("MeetingDate(%d) resolved a date, want none", tt.lesson)
			}
		})
	}
}

func TestMeetingDates(t *testing.T) {
	got := termCalendar().MeetingDates(3)
	want := []time.Time{
		date(2026, time.September, 8),
		date(2026, time.September, 10),
		date(2026, time.September, 15),
	}
	if len(got) != len(want) {
		t.Fatalf("MeetingDates returned %d dates, want %d", len(got), len(want))
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("meeting %d = %s, want %s", i+1, got[i], want[i])
		}
	}

	// A skipped meeting is not a meeting: the numbering slides past it rather than leaving a hole.
	skipped := Calendar{
		StartDate: date(2026, time.November, 24),
		Weekdays:  []int32{2, 4},
		Skip:      []time.Time{date(2026, time.November, 26)},
	}.MeetingDates(2)
	if len(skipped) != 2 || !skipped[1].Equal(date(2026, time.December, 1)) {
		t.Errorf("meeting 2 after a skip = %v, want 2026-12-01", skipped)
	}

	if (Calendar{}).MeetingDates(5) != nil {
		t.Error("MeetingDates on an unconfigured calendar returned dates")
	}
	if termCalendar().MeetingDates(0) != nil {
		t.Error("MeetingDates(0) returned dates")
	}
	if got := termCalendar().MeetingDates(MaxReleaseDay + 10); int32(len(got)) != MaxReleaseDay {
		t.Errorf("MeetingDates clamped to %d dates, want MaxReleaseDay (%d)", len(got), MaxReleaseDay)
	}
}
