package cal

import (
	"math"
	"strings"
	"testing"
	"time"
)

var paris, _ = time.LoadLocation("Europe/Paris")

func mustParse(t *testing.T, data string) string {
	t.Helper()
	return strings.ReplaceAll(strings.TrimSpace(data), "\n", "\r\n") + "\r\n"
}

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, paris)
	if err != nil {
		panic(err)
	}
	return t
}

const weekly = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:test
BEGIN:VEVENT
UID:weekly-1
DTSTAMP:20261001T000000Z
DTSTART;TZID=Europe/Paris:20261019T140000
DTEND;TZID=Europe/Paris:20261019T150000
RRULE:FREQ=WEEKLY;BYDAY=MO
EXDATE;TZID=Europe/Paris:20261102T140000
SUMMARY:Sport
END:VEVENT
BEGIN:VEVENT
UID:weekly-1
DTSTAMP:20261001T000000Z
RECURRENCE-ID;TZID=Europe/Paris:20261109T140000
DTSTART;TZID=Europe/Paris:20261110T090000
DTEND;TZID=Europe/Paris:20261110T100000
SUMMARY:Sport (moved)
END:VEVENT
END:VCALENDAR`

func TestExpandAcrossDSTWithExceptions(t *testing.T) {
	c, err := Parse(mustParse(t, weekly))
	if err != nil {
		t.Fatal(err)
	}
	list, err := Expand(c, paris, at("2026-10-19 00:00"), at("2026-11-17 00:00"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, o := range list {
		got = append(got, o.Start.In(paris).Format("01-02 15:04")+" "+o.Title)
	}
	want := []string{
		"10-19 14:00 Sport",
		"10-26 14:00 Sport", // after the DST change: still 14:00 local
		"11-10 09:00 Sport (moved)",
		"11-16 14:00 Sport",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if !list[2].RecurrenceID.Equal(at("2026-11-09 14:00")) {
		t.Errorf("override recurrence id = %v", list[2].RecurrenceID)
	}

	info, err := Inspect(c, paris)
	if err != nil {
		t.Fatal(err)
	}
	if info.UID != "weekly-1" || info.LastEnd != math.MaxInt64 || info.First != at("2026-10-19 14:00").Unix() {
		t.Errorf("info = %+v", info)
	}
}

func TestUpdateOccurrenceThenSeries(t *testing.T) {
	c, _ := Parse(mustParse(t, weekly))
	err := UpdateOccurrence(c, at("2026-10-26 14:00"), Fields{Title: "Gym", Start: at("2026-10-26 18:00"), End: at("2026-10-26 19:30")}, paris)
	if err != nil {
		t.Fatal(err)
	}
	// Move the whole series one hour later, from the 16 Nov occurrence.
	err = UpdateSeries(c, at("2026-11-16 14:00"), Fields{Title: "Running", Start: at("2026-11-16 15:00"), End: at("2026-11-16 16:00")}, paris)
	if err != nil {
		t.Fatal(err)
	}
	data, err := Encode(c)
	if err != nil {
		t.Fatal(err)
	}
	c, err = Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := Expand(c, paris, at("2026-10-19 00:00"), at("2026-11-24 00:00"))
	var got []string
	for _, o := range list {
		got = append(got, o.Start.In(paris).Format("01-02 15:04")+" "+o.Title)
	}
	want := []string{
		"10-19 15:00 Running",
		"10-26 18:00 Gym",           // its own title is kept
		"11-10 09:00 Sport (moved)", // so is this one, renamed before
		"11-16 15:00 Running",
		"11-23 15:00 Running",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if !strings.Contains(data, "EXDATE;TZID=Europe/Paris:20261102T150000") {
		t.Errorf("the exception did not move with the series:\n%s", data)
	}
	if !strings.Contains(data, "BEGIN:VTIMEZONE") {
		t.Errorf("no VTIMEZONE written:\n%s", data)
	}
}

func TestDeleteOccurrence(t *testing.T) {
	c, _ := Parse(mustParse(t, weekly))
	if !DeleteOccurrence(c, at("2026-11-09 14:00"), paris) {
		t.Fatal("object reported empty")
	}
	list, _ := Expand(c, paris, at("2026-11-09 00:00"), at("2026-11-12 00:00"))
	if len(list) != 0 {
		t.Fatalf("deleted occurrence still listed: %+v", list)
	}
}

func TestNewAllDayAndRemoveRule(t *testing.T) {
	rule := "FREQ=DAILY;UNTIL=20261103"
	c, err := New("day-1", Fields{Title: "Holidays", AllDay: true, Start: at("2026-11-01 00:00"), End: at("2026-11-02 00:00"), RRule: &rule}, paris)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := Expand(c, paris, at("2026-10-01 00:00"), at("2026-12-01 00:00"))
	if len(list) != 3 || !list[0].AllDay || list[2].Start.Day() != 3 {
		t.Fatalf("got %+v", list)
	}
	none := ""
	if err := UpdateSeries(c, list[1].RecurrenceID, Fields{Title: "Off", AllDay: true, Start: at("2026-11-02 00:00"), End: at("2026-11-03 00:00"), RRule: &none}, paris); err != nil {
		t.Fatal(err)
	}
	list, _ = Expand(c, paris, at("2026-10-01 00:00"), at("2026-12-01 00:00"))
	if len(list) != 1 || list[0].Recurring || list[0].Start.Day() != 2 {
		t.Fatalf("got %+v", list)
	}
}

func TestNormalizeRRuleUntil(t *testing.T) {
	got, err := NormalizeRRule("FREQ=WEEKLY;BYDAY=MO,WE;UNTIL=20261231", false, paris)
	if err != nil {
		t.Fatal(err)
	}
	if got != "FREQ=WEEKLY;BYDAY=MO,WE;UNTIL=20261231T225959Z" {
		t.Errorf("got %q", got)
	}
	if _, err := NormalizeRRule("FREQ=SOMETIMES", false, paris); err == nil {
		t.Error("invalid rule accepted")
	}
}

func TestVTimezoneParis(t *testing.T) {
	tz := vtimezone(paris, 2026)
	if len(tz.Children) != 2 {
		t.Fatalf("got %d components", len(tz.Children))
	}
	var rules []string
	for _, c := range tz.Children {
		rules = append(rules, c.Name+" "+c.Props.Get("DTSTART").Value+" "+c.Props.Get("RRULE").Value+" "+c.Props.Get("TZOFFSETTO").Value)
	}
	want := "DAYLIGHT 19700329T020000 FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU +0200|STANDARD 19701025T030000 FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU +0100"
	if strings.Join(rules, "|") != want {
		t.Errorf("got %q", rules)
	}
}

func TestFoldKeepsUTF8(t *testing.T) {
	line := "DESCRIPTION:" + strings.Repeat("é", 60) + "\r\n"
	folded := fold(line)
	for _, l := range strings.Split(folded, "\r\n") {
		if len(l) > 76 {
			t.Errorf("line too long: %d", len(l))
		}
	}
	if strings.ReplaceAll(folded, "\r\n ", "") != line {
		t.Error("unfolding does not give the line back")
	}
}

func TestFloatingAndPrefixedTZID(t *testing.T) {
	data := mustParse(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:test
BEGIN:VEVENT
UID:x
DTSTAMP:20261001T000000Z
DTSTART;TZID=/mozilla.org/20050126_1/Europe/Paris:20260710T100000
DURATION:PT30M
SUMMARY:A
END:VEVENT
END:VCALENDAR`)
	c, _ := Parse(data)
	list, err := Expand(c, time.UTC, at("2026-07-10 00:00"), at("2026-07-11 00:00"))
	if err != nil || len(list) != 1 {
		t.Fatalf("%v %+v", err, list)
	}
	if !list[0].Start.Equal(at("2026-07-10 10:00")) || list[0].End.Sub(list[0].Start) != 30*time.Minute {
		t.Errorf("got %v - %v", list[0].Start, list[0].End)
	}
}
