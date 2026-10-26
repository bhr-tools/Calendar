// Package cal reads, expands and edits iCalendar objects (RFC 5545). An object
// holds one event: a master VEVENT, possibly recurring, and the VEVENTs
// overriding some of its occurrences (same UID, with a RECURRENCE-ID).
package cal

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-ical"
)

// ProdID identifies this application in the objects it writes.
const ProdID = "-//bhr-tools//Calendar//EN"

// Parse decodes one iCalendar object.
func Parse(data string) (*ical.Calendar, error) {
	c, err := ical.NewDecoder(strings.NewReader(data)).Decode()
	if err != nil {
		return nil, fmt.Errorf("invalid iCalendar data: %w", err)
	}
	return c, nil
}

// Encode serializes an object, folding long lines as RFC 5545 asks.
func Encode(c *ical.Calendar) (string, error) {
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(c); err != nil {
		return "", err
	}
	return fold(buf.String()), nil
}

// fold splits lines longer than 75 octets, never inside a UTF-8 sequence.
func fold(s string) string {
	var out strings.Builder
	out.Grow(len(s) + len(s)/60)
	for _, line := range strings.SplitAfter(s, "\r\n") {
		for len(strings.TrimSuffix(line, "\r\n")) > 75 {
			cut := 75
			for cut > 0 && !utf8.RuneStart(line[cut]) {
				cut--
			}
			out.WriteString(line[:cut])
			out.WriteString("\r\n ")
			line = line[cut:]
		}
		out.WriteString(line)
	}
	return out.String()
}

// events returns the VEVENTs of an object.
func events(c *ical.Calendar) []*ical.Component {
	var list []*ical.Component
	for _, child := range c.Children {
		if child.Name == ical.CompEvent {
			list = append(list, child)
		}
	}
	return list
}

// split returns the master VEVENT (nil when the object only holds overridden
// occurrences, as in some invitations) and the overrides.
func split(c *ical.Calendar) (master *ical.Component, overrides []*ical.Component) {
	for _, e := range events(c) {
		if e.Props.Get(ical.PropRecurrenceID) != nil {
			overrides = append(overrides, e)
		} else if master == nil {
			master = e
		}
	}
	return master, overrides
}

// location resolves a TZID. Besides IANA names it accepts the prefixed forms
// some clients write ("/mozilla.org/20050126_1/Europe/Paris").
func location(tzid string, fallback *time.Location) *time.Location {
	if tzid == "" {
		return fallback
	}
	if loc, err := time.LoadLocation(tzid); err == nil {
		return loc
	}
	parts := strings.Split(strings.Trim(tzid, "/"), "/")
	for i := 1; i < len(parts); i++ {
		if loc, err := time.LoadLocation(strings.Join(parts[i:], "/")); err == nil {
			return loc
		}
	}
	return fallback
}

const (
	dateLayout     = "20060102"
	dateTimeLayout = "20060102T150405"
)

// times parses a DATE or DATE-TIME property, which may hold a comma-separated
// list (EXDATE, RDATE). Floating times and unknown time zones use loc.
func times(p *ical.Prop, loc *time.Location) ([]time.Time, bool, error) {
	allDay := p.Params.Get(ical.ParamValue) == string(ical.ValueDate)
	tz := location(p.Params.Get(ical.PropTimezoneID), loc)
	var list []time.Time
	for _, v := range strings.Split(p.Value, ",") {
		v = strings.TrimSpace(v)
		var t time.Time
		var err error
		switch {
		case len(v) == len(dateLayout):
			allDay = true
			t, err = time.ParseInLocation(dateLayout, v, loc)
		case strings.HasSuffix(v, "Z"):
			t, err = time.Parse(dateTimeLayout+"Z", v)
		default:
			t, err = time.ParseInLocation(dateTimeLayout, v, tz)
		}
		if err != nil {
			return nil, false, fmt.Errorf("invalid %s %q", p.Name, p.Value)
		}
		list = append(list, t)
	}
	return list, allDay, nil
}

// timeOf parses a single DATE or DATE-TIME property.
func timeOf(p *ical.Prop, loc *time.Location) (time.Time, bool, error) {
	list, allDay, err := times(p, loc)
	if err != nil {
		return time.Time{}, false, err
	}
	if len(list) != 1 {
		return time.Time{}, false, fmt.Errorf("invalid %s %q", p.Name, p.Value)
	}
	return list[0], allDay, nil
}

// span is the time of one VEVENT.
type span struct {
	start, end time.Time
	allDay     bool
	// loc is the time zone of DTSTART, used to expand recurrences.
	loc *time.Location
}

// days is the length of an all-day span in calendar days.
func (s span) days() int {
	return int(s.end.Sub(s.start).Round(24*time.Hour) / (24 * time.Hour))
}

// addLength returns the end of a span of the same length starting at start.
func (s span) addLength(start time.Time) time.Time {
	if s.allDay {
		return start.AddDate(0, 0, s.days())
	}
	return start.Add(s.end.Sub(s.start))
}

func spanOf(e *ical.Component, loc *time.Location) (span, error) {
	startProp := e.Props.Get(ical.PropDateTimeStart)
	if startProp == nil {
		return span{}, errors.New("event without DTSTART")
	}
	start, allDay, err := timeOf(startProp, loc)
	if err != nil {
		return span{}, err
	}
	s := span{start: start, allDay: allDay, loc: location(startProp.Params.Get(ical.PropTimezoneID), loc)}
	if strings.HasSuffix(startProp.Value, "Z") {
		s.loc = loc
	}
	switch {
	case e.Props.Get(ical.PropDateTimeEnd) != nil:
		end, _, err := timeOf(e.Props.Get(ical.PropDateTimeEnd), loc)
		if err != nil {
			return span{}, err
		}
		s.end = end
	case e.Props.Get(ical.PropDuration) != nil:
		d, err := e.Props.Get(ical.PropDuration).Duration()
		if err != nil {
			return span{}, fmt.Errorf("invalid DURATION: %w", err)
		}
		if allDay && d%(24*time.Hour) == 0 {
			s.end = start.AddDate(0, 0, int(d/(24*time.Hour)))
		} else {
			s.end = start.Add(d)
		}
	case allDay:
		s.end = start.AddDate(0, 0, 1)
	default:
		s.end = start
	}
	if s.end.Before(s.start) {
		s.end = s.start
	}
	return s, nil
}

func text(e *ical.Component, name string) string {
	p := e.Props.Get(name)
	if p == nil {
		return ""
	}
	v, err := p.Text()
	if err != nil {
		return p.Value
	}
	return v
}

func setText(e *ical.Component, name, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		e.Props.Del(name)
		return
	}
	e.Props.SetText(name, value)
}
