package cal

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

// Fields are the parts of an event edited by the web app. Everything else in
// an object (alarms, attendees, client-specific properties) is preserved.
type Fields struct {
	Title    string
	Location string
	Notes    string
	AllDay   bool
	// Start and End (exclusive). All-day events use midnights in the
	// editing time zone.
	Start, End time.Time
	// RRule is the recurrence rule ("" for none). Nil keeps the current one.
	RRule *string
}

func (f Fields) validate() error {
	if f.Start.IsZero() || f.End.IsZero() {
		return errors.New("the event needs a start and an end")
	}
	if f.End.Before(f.Start) {
		return errors.New("the event ends before it starts")
	}
	if f.AllDay && !f.End.After(f.Start) {
		return errors.New("an all-day event lasts at least one day")
	}
	return nil
}

// setTime writes a DATE or DATE-TIME property: dates for all-day events,
// local times with a TZID otherwise (UTC stays UTC).
func setTime(e *ical.Component, name string, t time.Time, allDay bool, loc *time.Location) {
	p := ical.NewProp(name)
	if allDay {
		p.SetDate(t.In(loc))
	} else {
		p.SetDateTime(t.In(loc))
	}
	e.Props.Set(p)
}

// formatLike formats t as the value of a property shaped like ref (DTSTART of
// the master): same value type and time zone. Used for EXDATE and
// RECURRENCE-ID, which must match DTSTART.
func formatLike(name string, ref *ical.Prop, t time.Time, loc *time.Location) *ical.Prop {
	p := ical.NewProp(name)
	switch {
	case ref.Params.Get(ical.ParamValue) == string(ical.ValueDate) || len(ref.Value) == len(dateLayout):
		p.SetDate(t.In(loc))
	case strings.HasSuffix(ref.Value, "Z"):
		p.SetDateTime(t.UTC())
	case ref.Params.Get(ical.PropTimezoneID) != "":
		p.Params.Set(ical.PropTimezoneID, ref.Params.Get(ical.PropTimezoneID))
		p.SetValueType(ical.ValueDateTime)
		p.Value = t.In(location(ref.Params.Get(ical.PropTimezoneID), loc)).Format(dateTimeLayout)
	default:
		p.SetValueType(ical.ValueDateTime)
		p.Value = t.In(loc).Format(dateTimeLayout)
	}
	return p
}

func touch(e *ical.Component) {
	stamp := time.Now().UTC()
	e.Props.SetDateTime(ical.PropDateTimeStamp, stamp)
	e.Props.SetDateTime(ical.PropLastModified, stamp)
	seq := 0
	if p := e.Props.Get(ical.PropSequence); p != nil {
		seq, _ = strconv.Atoi(p.Value)
	}
	e.Props.Set(&ical.Prop{Name: ical.PropSequence, Params: ical.Params{}, Value: strconv.Itoa(seq + 1)})
}

func setSpan(e *ical.Component, f Fields, loc *time.Location) {
	e.Props.Del(ical.PropDuration)
	setTime(e, ical.PropDateTimeStart, f.Start, f.AllDay, loc)
	setTime(e, ical.PropDateTimeEnd, f.End, f.AllDay, loc)
}

func setTexts(e *ical.Component, f Fields) {
	setText(e, ical.PropSummary, f.Title)
	setText(e, ical.PropLocation, f.Location)
	setText(e, ical.PropDescription, f.Notes)
}

// NormalizeRRule validates a rule written by the web app and makes its UNTIL
// match the event: a date for all-day events, the end of that day in UTC
// otherwise (RFC 5545 requires UTC with a zoned DTSTART).
func NormalizeRRule(rule string, allDay bool, loc *time.Location) (string, error) {
	rule = strings.TrimPrefix(strings.TrimSpace(rule), "RRULE:")
	if rule == "" {
		return "", nil
	}
	if _, err := rrule.StrToROptionInLocation(rule, loc); err != nil {
		return "", fmt.Errorf("invalid recurrence: %w", err)
	}
	parts := strings.Split(rule, ";")
	for i, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		if !ok || !strings.EqualFold(key, "UNTIL") {
			continue
		}
		day, err := time.ParseInLocation(dateLayout, value[:min(len(value), 8)], loc)
		if err != nil {
			return "", fmt.Errorf("invalid recurrence end %q", value)
		}
		if allDay {
			parts[i] = "UNTIL=" + day.Format(dateLayout)
		} else {
			parts[i] = "UNTIL=" + day.AddDate(0, 0, 1).Add(-time.Second).UTC().Format(dateTimeLayout+"Z")
		}
	}
	return strings.Join(parts, ";"), nil
}

// New builds an object holding a new event. Times are written in loc.
func New(uid string, f Fields, loc *time.Location) (*ical.Calendar, error) {
	if err := f.validate(); err != nil {
		return nil, err
	}
	c := ical.NewCalendar()
	c.Props.SetText(ical.PropProductID, ProdID)
	c.Props.SetText(ical.PropVersion, "2.0")
	e := ical.NewComponent(ical.CompEvent)
	e.Props.SetText(ical.PropUID, uid)
	e.Props.SetDateTime(ical.PropCreated, time.Now().UTC())
	setTexts(e, f)
	setSpan(e, f, loc)
	if f.RRule != nil && *f.RRule != "" {
		rule, err := NormalizeRRule(*f.RRule, f.AllDay, loc)
		if err != nil {
			return nil, err
		}
		e.Props.Set(&ical.Prop{Name: ical.PropRecurrenceRule, Params: ical.Params{}, Value: rule})
	}
	touch(e)
	c.Children = append(c.Children, e)
	ensureTimezones(c)
	return c, nil
}

// UpdateSeries applies f to a whole event. For a recurring event, occurrence
// is the edited occurrence: the series moves by as much as it moved.
func UpdateSeries(c *ical.Calendar, occurrence time.Time, f Fields, loc *time.Location) error {
	if err := f.validate(); err != nil {
		return err
	}
	master, overrides := split(c)
	if master == nil {
		if len(overrides) == 1 {
			return UpdateOccurrence(c, mustRecurrenceID(overrides[0], loc), f, loc)
		}
		return errors.New("this invitation only holds some occurrences: edit them one by one")
	}
	ms, err := spanOf(master, loc)
	if err != nil {
		return err
	}
	set, err := recurrenceSet(master, ms)
	if err != nil {
		return err
	}
	// Keep the event's own time zone when it has a known one.
	zone := loc
	if !ms.allDay && master.Props.Get(ical.PropDateTimeStart).Params.Get(ical.PropTimezoneID) != "" {
		zone = ms.loc
	}

	oldTitle, oldLocation, oldNotes := text(master, ical.PropSummary), text(master, ical.PropLocation), text(master, ical.PropDescription)
	setTexts(master, f)
	for _, e := range overrides {
		// Overrides follow the series for the fields they did not change.
		if text(e, ical.PropSummary) == oldTitle {
			setText(e, ical.PropSummary, f.Title)
		}
		if text(e, ical.PropLocation) == oldLocation {
			setText(e, ical.PropLocation, f.Location)
		}
		if text(e, ical.PropDescription) == oldNotes {
			setText(e, ical.PropDescription, f.Notes)
		}
		touch(e)
	}

	removeRule := f.RRule != nil && *f.RRule == ""
	switch {
	case set == nil || removeRule:
		// Not (or no longer) recurring: the event takes the edited times.
		setSpan(master, Fields{AllDay: f.AllDay, Start: f.Start, End: f.End}, zone)
		if removeRule {
			master.Props.Del(ical.PropRecurrenceRule)
			master.Props.Del(ical.PropRecurrenceDates)
			master.Props.Del(ical.PropExceptionDates)
			c.Children = withoutOverrides(c.Children)
		}
	default:
		// Move the series by the shift of the edited occurrence, and its
		// exceptions with it.
		current := Occurrence{Start: occurrence}
		if !occurrence.IsZero() {
			if current, err = Find(c, loc, occurrence); err != nil {
				return err
			}
		} else {
			current.Start = ms.start
		}
		shift := f.Start.Sub(current.Start)
		days := 0
		if f.AllDay && ms.allDay {
			days = int(f.Start.Sub(current.Start).Round(24*time.Hour) / (24 * time.Hour))
		}
		move := func(t time.Time) time.Time {
			if days != 0 || (f.AllDay && ms.allDay) {
				return t.AddDate(0, 0, days)
			}
			return t.Add(shift)
		}
		newStart := move(ms.start)
		if f.AllDay != ms.allDay {
			// Switching between all-day and timed: keep the day, take the
			// edited time of day.
			y, m, d := ms.start.In(loc).Date()
			h, mi, _ := f.Start.In(loc).Clock()
			if f.AllDay {
				h, mi = 0, 0
			}
			newStart = time.Date(y, m, d, h, mi, 0, 0, loc)
		}
		setSpan(master, Fields{AllDay: f.AllDay, Start: newStart, End: newStart.Add(f.End.Sub(f.Start))}, zone)
		if f.AllDay {
			setTime(master, ical.PropDateTimeEnd, newStart.AddDate(0, 0, max(1, int(f.End.Sub(f.Start).Round(24*time.Hour)/(24*time.Hour)))), true, zone)
		}
		ref := master.Props.Get(ical.PropDateTimeStart)
		var exdates []ical.Prop
		for _, p := range master.Props.Values(ical.PropExceptionDates) {
			list, _, err := times(&p, ms.loc)
			if err != nil {
				return err
			}
			for _, t := range list {
				exdates = append(exdates, *formatLike(ical.PropExceptionDates, ref, move(t), zone))
			}
		}
		master.Props.Del(ical.PropExceptionDates)
		for _, p := range exdates {
			master.Props.Add(&p)
		}
		for _, e := range overrides {
			id, err := recurrenceIDOf(e, ms.loc)
			if err != nil {
				return err
			}
			e.Props.Set(formatLike(ical.PropRecurrenceID, ref, move(id), zone))
		}
	}

	if f.RRule != nil && *f.RRule != "" {
		rule, err := NormalizeRRule(*f.RRule, f.AllDay, zone)
		if err != nil {
			return err
		}
		master.Props.Set(&ical.Prop{Name: ical.PropRecurrenceRule, Params: ical.Params{}, Value: rule})
	}
	touch(master)
	ensureTimezones(c)
	return nil
}

func withoutOverrides(children []*ical.Component) []*ical.Component {
	var kept []*ical.Component
	for _, child := range children {
		if child.Name == ical.CompEvent && child.Props.Get(ical.PropRecurrenceID) != nil {
			continue
		}
		kept = append(kept, child)
	}
	return kept
}

func mustRecurrenceID(e *ical.Component, loc *time.Location) time.Time {
	t, _ := recurrenceIDOf(e, loc)
	return t
}

// UpdateOccurrence applies f to one occurrence of a recurring event, adding
// an override for it when there is none yet.
func UpdateOccurrence(c *ical.Calendar, recurrenceID time.Time, f Fields, loc *time.Location) error {
	if err := f.validate(); err != nil {
		return err
	}
	master, overrides := split(c)
	var target *ical.Component
	for _, e := range overrides {
		if id, err := recurrenceIDOf(e, loc); err == nil && id.Equal(recurrenceID) {
			target = e
		}
	}
	zone := loc
	if target == nil {
		if master == nil {
			return errors.New("occurrence not found")
		}
		target = ical.NewComponent(ical.CompEvent)
		for name, props := range master.Props {
			switch name {
			case ical.PropRecurrenceRule, ical.PropRecurrenceDates, ical.PropExceptionDates,
				ical.PropDateTimeStart, ical.PropDateTimeEnd, ical.PropDuration, ical.PropSequence:
				continue
			}
			target.Props[name] = append([]ical.Prop(nil), props...)
		}
		for _, child := range master.Children {
			target.Children = append(target.Children, child)
		}
		target.Props.Set(formatLike(ical.PropRecurrenceID, master.Props.Get(ical.PropDateTimeStart), recurrenceID, loc))
		c.Children = append(c.Children, target)
	}
	if master != nil {
		if ms, err := spanOf(master, loc); err == nil && !ms.allDay && master.Props.Get(ical.PropDateTimeStart).Params.Get(ical.PropTimezoneID) != "" {
			zone = ms.loc
		}
	}
	setTexts(target, f)
	setSpan(target, f, zone)
	touch(target)
	ensureTimezones(c)
	return nil
}

// DeleteOccurrence removes one occurrence of a recurring event. It reports
// whether the object still holds an event; if not, delete the object.
func DeleteOccurrence(c *ical.Calendar, recurrenceID time.Time, loc *time.Location) bool {
	master, _ := split(c)
	var kept []*ical.Component
	for _, child := range c.Children {
		if child.Name == ical.CompEvent && child.Props.Get(ical.PropRecurrenceID) != nil {
			if id, err := recurrenceIDOf(child, loc); err == nil && id.Equal(recurrenceID) {
				continue
			}
		}
		kept = append(kept, child)
	}
	c.Children = kept
	if master != nil {
		master.Props.Add(formatLike(ical.PropExceptionDates, master.Props.Get(ical.PropDateTimeStart), recurrenceID, loc))
		touch(master)
	}
	return len(events(c)) > 0
}
