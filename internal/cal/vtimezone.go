package cal

import (
	"fmt"
	"time"

	"github.com/emersion/go-ical"
)

// ensureTimezones adds a VTIMEZONE for every TZID used by the object's events
// that has none, as RFC 5545 requires.
func ensureTimezones(c *ical.Calendar) {
	have := map[string]bool{}
	for _, child := range c.Children {
		if child.Name == ical.CompTimezone {
			have[text(child, ical.PropTimezoneID)] = true
		}
	}
	used := map[string]bool{}
	for _, e := range events(c) {
		for _, props := range e.Props {
			for _, p := range props {
				if tzid := p.Params.Get(ical.PropTimezoneID); tzid != "" {
					used[tzid] = true
				}
			}
		}
	}
	var zones []*ical.Component
	for tzid := range used {
		if have[tzid] {
			continue
		}
		loc, err := time.LoadLocation(tzid)
		if err != nil {
			continue
		}
		zones = append(zones, vtimezone(loc, time.Now().Year()))
	}
	if len(zones) > 0 {
		c.Children = append(zones, c.Children...)
	}
}

// vtimezone describes loc with yearly rules derived from its transitions in
// year: the usual shape for zones with daylight saving time ("last Sunday of
// March"), or a single fixed offset.
func vtimezone(loc *time.Location, year int) *ical.Component {
	tz := ical.NewComponent(ical.CompTimezone)
	tz.Props.SetText(ical.PropTimezoneID, loc.String())

	transitions := yearTransitions(loc, year)
	if len(transitions) == 0 {
		name, offset := time.Date(year, 1, 1, 0, 0, 0, 0, loc).Zone()
		std := ical.NewComponent(ical.CompTimezoneStandard)
		setRaw(std, ical.PropDateTimeStart, "19700101T000000")
		setRaw(std, ical.PropTimezoneOffsetFrom, utcOffset(offset))
		setRaw(std, ical.PropTimezoneOffsetTo, utcOffset(offset))
		setRaw(std, ical.PropTimezoneName, name)
		tz.Children = append(tz.Children, std)
		return tz
	}
	for _, at := range transitions {
		_, before := at.Add(-time.Second).Zone()
		name, after := at.Zone()
		kind := ical.CompTimezoneStandard
		if after > before {
			kind = ical.CompTimezoneDaylight
		}
		// The rule is expressed in local time before the transition.
		local := at.In(time.FixedZone("", before))
		nth := (local.Day()-1)/7 + 1
		if local.AddDate(0, 0, 7).Month() != local.Month() {
			nth = -1
		}
		weekday := [...]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}[local.Weekday()]
		first := firstMatch(1970, local.Month(), local.Weekday(), nth)

		comp := ical.NewComponent(kind)
		setRaw(comp, ical.PropDateTimeStart, fmt.Sprintf("1970%02d%02dT%02d%02d%02d", local.Month(), first, local.Hour(), local.Minute(), local.Second()))
		setRaw(comp, ical.PropRecurrenceRule, fmt.Sprintf("FREQ=YEARLY;BYMONTH=%d;BYDAY=%d%s", local.Month(), nth, weekday))
		setRaw(comp, ical.PropTimezoneOffsetFrom, utcOffset(before))
		setRaw(comp, ical.PropTimezoneOffsetTo, utcOffset(after))
		setRaw(comp, ical.PropTimezoneName, name)
		tz.Children = append(tz.Children, comp)
	}
	return tz
}

func setRaw(comp *ical.Component, name, value string) {
	comp.Props.Set(&ical.Prop{Name: name, Params: ical.Params{}, Value: value})
}

// yearTransitions returns the instants where loc changes its offset in year.
func yearTransitions(loc *time.Location, year int) []time.Time {
	var list []time.Time
	t := time.Date(year, 1, 1, 0, 0, 0, 0, loc)
	end := time.Date(year+1, 1, 1, 0, 0, 0, 0, loc)
	_, offset := t.Zone()
	for t.Before(end) {
		next := t.Add(24 * time.Hour)
		if _, o := next.Zone(); o != offset {
			// Narrow down to the second.
			lo, hi := t, next
			for hi.Sub(lo) > time.Second {
				mid := lo.Add(hi.Sub(lo) / 2)
				if _, o := mid.Zone(); o == offset {
					lo = mid
				} else {
					hi = mid
				}
			}
			list = append(list, hi)
			_, offset = next.Zone()
		}
		t = next
	}
	return list
}

// firstMatch returns the day of month of the nth weekday (-1: last) of month
// in year.
func firstMatch(year int, month time.Month, weekday time.Weekday, nth int) int {
	if nth > 0 {
		d := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
		for d.Weekday() != weekday {
			d = d.AddDate(0, 0, 1)
		}
		return d.AddDate(0, 0, 7*(nth-1)).Day()
	}
	d := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC)
	for d.Weekday() != weekday {
		d = d.AddDate(0, 0, -1)
	}
	return d.Day()
}

func utcOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign, seconds = "-", -seconds
	}
	return fmt.Sprintf("%s%02d%02d", sign, seconds/3600, seconds/60%60)
}
