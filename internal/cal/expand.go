package cal

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

// maxOccurrences caps the expansion of a bounded recurrence (COUNT or UNTIL)
// when computing where it ends.
const maxOccurrences = 20000

// Occurrence is one instance of an event.
type Occurrence struct {
	UID      string
	Title    string
	Location string
	Notes    string
	AllDay   bool
	// Start and End (exclusive). All-day occurrences start at midnight.
	Start, End time.Time
	// Recurring events only: the original start of this occurrence, which
	// identifies it, and the rule of the series.
	Recurring    bool
	RecurrenceID time.Time
	RRule        string
	Cancelled    bool
}

// Info summarizes an object for storage and queries.
type Info struct {
	UID     string
	Title   string
	Search  string
	First   int64 // Unix seconds
	LastEnd int64 // Unix seconds, math.MaxInt64 for endless recurrences
}

// recurrenceSet builds the recurrence of a master VEVENT, or nil when it does
// not repeat.
func recurrenceSet(master *ical.Component, s span) (*rrule.Set, error) {
	ruleProp := master.Props.Get(ical.PropRecurrenceRule)
	rdates := master.Props.Values(ical.PropRecurrenceDates)
	if ruleProp == nil && len(rdates) == 0 {
		return nil, nil
	}
	set := &rrule.Set{}
	start := s.start.In(s.loc)
	if ruleProp != nil {
		opt, err := rrule.StrToROptionInLocation(ruleProp.Value, s.loc)
		if err != nil {
			return nil, fmt.Errorf("invalid RRULE %q: %w", ruleProp.Value, err)
		}
		opt.Dtstart = start
		rule, err := rrule.NewRRule(*opt)
		if err != nil {
			return nil, fmt.Errorf("invalid RRULE %q: %w", ruleProp.Value, err)
		}
		set.RRule(rule)
	} else {
		// RDATE alone: DTSTART is the first occurrence.
		set.RDate(start)
	}
	set.DTStart(start)
	for _, p := range rdates {
		list, _, err := times(&p, s.loc)
		if err != nil {
			// RDATE;VALUE=PERIOD is not supported: ignore it.
			continue
		}
		for _, t := range list {
			set.RDate(t)
		}
	}
	for _, p := range master.Props.Values(ical.PropExceptionDates) {
		list, _, err := times(&p, s.loc)
		if err != nil {
			return nil, err
		}
		for _, t := range list {
			set.ExDate(t)
		}
	}
	return set, nil
}

// endless reports whether a master VEVENT repeats forever.
func endless(master *ical.Component) bool {
	p := master.Props.Get(ical.PropRecurrenceRule)
	if p == nil {
		return false
	}
	v := strings.ToUpper(p.Value)
	return !strings.Contains(v, "COUNT=") && !strings.Contains(v, "UNTIL=")
}

func recurrenceIDOf(e *ical.Component, loc *time.Location) (time.Time, error) {
	t, _, err := timeOf(e.Props.Get(ical.PropRecurrenceID), loc)
	return t, err
}

// Inspect validates an object and summarizes it. loc interprets floating
// times.
func Inspect(c *ical.Calendar, loc *time.Location) (Info, error) {
	evs := events(c)
	if len(evs) == 0 {
		return Info{}, errors.New("the object holds no event (only VEVENT is supported)")
	}
	var info Info
	for _, e := range evs {
		uid := text(e, ical.PropUID)
		if uid == "" {
			return Info{}, errors.New("event without UID")
		}
		if info.UID == "" {
			info.UID = uid
		} else if uid != info.UID {
			return Info{}, errors.New("all the events of an object must share one UID")
		}
	}

	master, overrides := split(c)
	first, last := int64(math.MaxInt64), int64(math.MinInt64)
	extend := func(start, end time.Time) {
		first = min(first, start.Unix())
		last = max(last, end.Unix())
	}
	for _, e := range overrides {
		s, err := spanOf(e, loc)
		if err != nil {
			return Info{}, err
		}
		if _, err := recurrenceIDOf(e, loc); err != nil {
			return Info{}, err
		}
		extend(s.start, s.end)
	}
	if master != nil {
		s, err := spanOf(master, loc)
		if err != nil {
			return Info{}, err
		}
		set, err := recurrenceSet(master, s)
		if err != nil {
			return Info{}, err
		}
		extend(s.start, s.end)
		switch {
		case set == nil:
		case endless(master):
			last = math.MaxInt64
		default:
			next := set.Iterator()
			for i := 0; i < maxOccurrences; i++ {
				t, ok := next()
				if !ok {
					break
				}
				extend(t, s.addLength(t))
			}
		}
		info.Title = text(master, ical.PropSummary)
	} else {
		info.Title = text(overrides[0], ical.PropSummary)
	}
	if last == first {
		// Zero-length events still need to be found by range queries.
		last++
	}
	info.First, info.LastEnd = first, last

	var words []string
	for _, e := range evs {
		words = append(words, text(e, ical.PropSummary), text(e, ical.PropLocation), text(e, ical.PropDescription))
	}
	info.Search = strings.ToLower(strings.Join(words, " "))
	return info, nil
}

func occurrence(e *ical.Component, s span) Occurrence {
	return Occurrence{
		UID:       text(e, ical.PropUID),
		Title:     text(e, ical.PropSummary),
		Location:  text(e, ical.PropLocation),
		Notes:     text(e, ical.PropDescription),
		AllDay:    s.allDay,
		Start:     s.start,
		End:       s.end,
		Cancelled: strings.EqualFold(text(e, ical.PropStatus), "CANCELLED"),
	}
}

func overlaps(start, end, from, to time.Time) bool {
	if end.Equal(start) {
		return !start.Before(from) && start.Before(to)
	}
	return start.Before(to) && end.After(from)
}

// Expand returns the occurrences of an object overlapping [from, to), sorted
// by start.
func Expand(c *ical.Calendar, loc *time.Location, from, to time.Time) ([]Occurrence, error) {
	master, overrides := split(c)
	var list []Occurrence

	byID := map[int64]*ical.Component{}
	for _, e := range overrides {
		id, err := recurrenceIDOf(e, loc)
		if err != nil {
			return nil, err
		}
		byID[id.Unix()] = e
	}

	var set *rrule.Set
	var ms span
	if master != nil {
		var err error
		if ms, err = spanOf(master, loc); err != nil {
			return nil, err
		}
		if set, err = recurrenceSet(master, ms); err != nil {
			return nil, err
		}
	}

	if set == nil {
		if master != nil && overlaps(ms.start, ms.end, from, to) {
			list = append(list, occurrence(master, ms))
		}
		// Overridden occurrences of a series we do not have.
		for _, e := range overrides {
			s, err := spanOf(e, loc)
			if err != nil {
				return nil, err
			}
			if overlaps(s.start, s.end, from, to) {
				o := occurrence(e, s)
				o.Recurring = true
				o.RecurrenceID, _ = recurrenceIDOf(e, loc)
				list = append(list, o)
			}
		}
		return list, nil
	}

	rule := ""
	if p := master.Props.Get(ical.PropRecurrenceRule); p != nil {
		rule = p.Value
	}
	used := map[int64]bool{}
	add := func(o Occurrence, id time.Time) {
		o.Recurring, o.RecurrenceID, o.RRule = true, id, rule
		list = append(list, o)
	}

	// Start early enough to catch occurrences that began before `from`.
	length := ms.end.Sub(ms.start) + 24*time.Hour
	for _, t := range set.Between(from.Add(-length), to, true) {
		if e, ok := byID[t.Unix()]; ok {
			used[t.Unix()] = true
			s, err := spanOf(e, loc)
			if err != nil {
				return nil, err
			}
			if overlaps(s.start, s.end, from, to) {
				add(occurrence(e, s), t)
			}
			continue
		}
		end := ms.addLength(t)
		if overlaps(t, end, from, to) {
			o := occurrence(master, ms)
			o.Start, o.End = t, end
			add(o, t)
		}
	}
	// Occurrences moved into the range from outside of it.
	for id, e := range byID {
		if used[id] {
			continue
		}
		s, err := spanOf(e, loc)
		if err != nil {
			return nil, err
		}
		if overlaps(s.start, s.end, from, to) {
			rid, _ := recurrenceIDOf(e, loc)
			add(occurrence(e, s), rid)
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Start.Before(list[j].Start) })
	return list, nil
}

// Find returns the occurrence identified by recurrenceID (zero for a
// non-recurring event).
func Find(c *ical.Calendar, loc *time.Location, recurrenceID time.Time) (Occurrence, error) {
	master, overrides := split(c)
	if recurrenceID.IsZero() {
		if master == nil {
			return Occurrence{}, errors.New("event not found")
		}
		s, err := spanOf(master, loc)
		if err != nil {
			return Occurrence{}, err
		}
		o := occurrence(master, s)
		// The first occurrence of a series is identified like the others.
		if p := master.Props.Get(ical.PropRecurrenceRule); p != nil {
			o.Recurring, o.RecurrenceID, o.RRule = true, s.start, p.Value
		} else if master.Props.Get(ical.PropRecurrenceDates) != nil {
			o.Recurring, o.RecurrenceID = true, s.start
		}
		return o, nil
	}
	for _, e := range overrides {
		id, err := recurrenceIDOf(e, loc)
		if err == nil && id.Equal(recurrenceID) {
			s, err := spanOf(e, loc)
			if err != nil {
				return Occurrence{}, err
			}
			o := occurrence(e, s)
			o.Recurring, o.RecurrenceID = true, id
			return o, nil
		}
	}
	if master == nil {
		return Occurrence{}, errors.New("occurrence not found")
	}
	s, err := spanOf(master, loc)
	if err != nil {
		return Occurrence{}, err
	}
	o := occurrence(master, s)
	o.Start, o.End = recurrenceID, s.addLength(recurrenceID)
	o.Recurring, o.RecurrenceID = true, recurrenceID
	if p := master.Props.Get(ical.PropRecurrenceRule); p != nil {
		o.RRule = p.Value
	}
	return o, nil
}
