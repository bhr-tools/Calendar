// Package events edits calendars at the level of events and occurrences, on
// top of the stored iCalendar objects. Both the JSON API and CalDAV write
// through it, so every object is validated and indexed the same way.
package events

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/google/uuid"

	"github.com/bhr-tools/calendar/internal/cal"
	"github.com/bhr-tools/calendar/internal/db"
)

// Service is safe for concurrent use.
type Service struct {
	Store *db.Store
	// Loc interprets floating times (events without a time zone).
	Loc *time.Location
}

// InvalidError reports iCalendar data or event fields that cannot be stored.
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

func invalid(err error) error {
	return &InvalidError{Msg: err.Error()}
}

// Put stores iCalendar data under calendarID/name, validating it first.
func (s *Service) Put(calendarID, name, data string, cond db.Condition) (*db.Object, bool, error) {
	c, err := cal.Parse(data)
	if err != nil {
		return nil, false, invalid(err)
	}
	return s.put(calendarID, name, data, c, cond)
}

func (s *Service) put(calendarID, name, data string, c *ical.Calendar, cond db.Condition) (*db.Object, bool, error) {
	info, err := cal.Inspect(c, s.Loc)
	if err != nil {
		return nil, false, invalid(err)
	}
	return s.Store.PutObject(db.Object{
		CalendarID: calendarID,
		Name:       name,
		UID:        info.UID,
		Data:       data,
		Search:     info.Search,
		FirstStart: info.First,
		LastEnd:    info.LastEnd,
	}, cond)
}

func (s *Service) save(o *db.Object, c *ical.Calendar) (*db.Object, error) {
	data, err := cal.Encode(c)
	if err != nil {
		return nil, invalid(err)
	}
	saved, _, err := s.put(o.CalendarID, o.Name, data, c, db.Condition{IfMatch: o.ETag})
	return saved, err
}

// Occurrence is an occurrence with the object it comes from.
type Occurrence struct {
	cal.Occurrence
	CalendarID string
	Name       string
	ReadOnly   bool
}

// Between returns the occurrences of the given calendars overlapping
// [from, to), sorted by start. Objects that cannot be read are skipped.
func (s *Service) Between(calendars []db.Calendar, from, to time.Time) ([]Occurrence, error) {
	list := []Occurrence{}
	for _, c := range calendars {
		objs, err := s.Store.ObjectsInRange(c.ID, from.Unix(), to.Unix())
		if err != nil {
			return nil, err
		}
		for _, o := range objs {
			parsed, err := cal.Parse(o.Data)
			if err != nil {
				continue
			}
			occs, err := cal.Expand(parsed, s.Loc, from, to)
			if err != nil {
				continue
			}
			for _, occ := range occs {
				list = append(list, Occurrence{Occurrence: occ, CalendarID: c.ID, Name: o.Name, ReadOnly: c.ReadOnly()})
			}
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Start.Before(list[j].Start) })
	return list, nil
}

// Create adds an event to a calendar.
func (s *Service) Create(calendarID string, f cal.Fields, loc *time.Location) (*db.Object, error) {
	uid := uuid.NewString()
	c, err := cal.New(uid, f, loc)
	if err != nil {
		return nil, invalid(err)
	}
	data, err := cal.Encode(c)
	if err != nil {
		return nil, invalid(err)
	}
	o, _, err := s.put(calendarID, uid+".ics", data, c, db.Condition{IfNoneMatch: true})
	return o, err
}

func (s *Service) load(calendarID, name string) (*db.Object, *ical.Calendar, error) {
	o, err := s.Store.GetObject(calendarID, name)
	if err != nil {
		return nil, nil, err
	}
	c, err := cal.Parse(o.Data)
	if err != nil {
		return nil, nil, invalid(err)
	}
	return o, c, nil
}

// Scope tells which occurrences of a recurring event an edit applies to.
type Scope string

const (
	ScopeAll  Scope = "all"
	ScopeThis Scope = "this"
)

// Update edits an event. For a recurring event, recurrenceID names the
// edited occurrence and scope says whether only it or the whole series
// changes. A different targetCalendar moves the whole event there.
func (s *Service) Update(calendarID, name string, recurrenceID time.Time, scope Scope, f cal.Fields, targetCalendar string, loc *time.Location) (*db.Object, error) {
	o, c, err := s.load(calendarID, name)
	if err != nil {
		return nil, err
	}
	if scope == ScopeThis && !recurrenceID.IsZero() {
		if targetCalendar != "" && targetCalendar != calendarID {
			return nil, &InvalidError{Msg: "Only a whole series can move to another calendar"}
		}
		err = cal.UpdateOccurrence(c, recurrenceID, f, loc)
	} else {
		err = cal.UpdateSeries(c, recurrenceID, f, loc)
	}
	if err != nil {
		return nil, invalid(err)
	}
	saved, err := s.save(o, c)
	if err != nil {
		return nil, err
	}
	if targetCalendar != "" && targetCalendar != calendarID {
		return s.Store.MoveObject(calendarID, name, targetCalendar, "", db.Condition{IfMatch: saved.ETag})
	}
	return saved, nil
}

// Delete removes an event, or one occurrence of a recurring event.
func (s *Service) Delete(calendarID, name string, recurrenceID time.Time, scope Scope) error {
	if scope != ScopeThis || recurrenceID.IsZero() {
		return s.Store.DeleteObject(calendarID, name, db.Condition{})
	}
	o, c, err := s.load(calendarID, name)
	if err != nil {
		return err
	}
	if !cal.DeleteOccurrence(c, recurrenceID, s.Loc) {
		return s.Store.DeleteObject(calendarID, name, db.Condition{IfMatch: o.ETag})
	}
	_, err = s.save(o, c)
	return err
}

// Search returns the occurrences matching query, closest to now first: the
// next occurrence of each matching event, or its last one when it is over.
func (s *Service) Search(calendars []db.Calendar, query string, limit int) ([]Occurrence, error) {
	byID := map[string]db.Calendar{}
	for _, c := range calendars {
		byID[c.ID] = c
	}
	objs, err := s.Store.SearchObjects(query, 200)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	words := strings.Fields(strings.ToLower(query))
	var list []Occurrence
	for _, o := range objs {
		c, ok := byID[o.CalendarID]
		if !ok {
			continue
		}
		parsed, err := cal.Parse(o.Data)
		if err != nil {
			continue
		}
		occ, ok := closest(parsed, s.Loc, now, words)
		if ok {
			list = append(list, Occurrence{Occurrence: occ, CalendarID: c.ID, Name: o.Name, ReadOnly: c.ReadOnly()})
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		return distance(list[i].Start, now) < distance(list[j].Start, now)
	})
	if len(list) > limit {
		list = list[:limit]
	}
	return list, nil
}

func distance(t, now time.Time) time.Duration {
	d := t.Sub(now)
	if d < 0 {
		// Past events rank after upcoming ones at the same distance.
		return -d + time.Hour
	}
	return d
}

func matches(o cal.Occurrence, words []string) bool {
	text := strings.ToLower(o.Title + " " + o.Location + " " + o.Notes)
	for _, w := range words {
		if !strings.Contains(text, w) {
			return false
		}
	}
	return true
}

// closest finds the matching occurrence nearest to now, looking one year
// around it.
func closest(c *ical.Calendar, loc *time.Location, now time.Time, words []string) (cal.Occurrence, bool) {
	occs, err := cal.Expand(c, loc, now.AddDate(-1, 0, 0), now.AddDate(1, 0, 0))
	if err != nil {
		return cal.Occurrence{}, false
	}
	var best cal.Occurrence
	found := false
	for _, occ := range occs {
		if !matches(occ, words) {
			continue
		}
		if !found || distance(occ.Start, now) < distance(best.Start, now) {
			best, found = occ, true
		}
	}
	if !found {
		// Older events: show their first occurrence.
		all, err := cal.Expand(c, loc, time.Unix(0, 0), now.AddDate(-1, 0, 0))
		if err == nil {
			for _, occ := range all {
				if matches(occ, words) {
					return occ, true
				}
			}
		}
	}
	return best, found
}

// IsInvalid reports whether err is an InvalidError.
func IsInvalid(err error) bool {
	var e *InvalidError
	return errors.As(err, &e)
}

// Export writes a calendar as one iCalendar file, readable by any calendar
// app.
func Export(store *db.Store, c *db.Calendar) (string, error) {
	objs, err := store.ListObjects(c.ID)
	if err != nil {
		return "", err
	}
	out := ical.NewCalendar()
	out.Props.SetText(ical.PropProductID, cal.ProdID)
	out.Props.SetText(ical.PropVersion, "2.0")
	out.Props.SetText("X-WR-CALNAME", c.Name)
	zones := map[string]bool{}
	var timezones, events []*ical.Component
	for _, o := range objs {
		parsed, err := cal.Parse(o.Data)
		if err != nil {
			continue
		}
		for _, child := range parsed.Children {
			switch child.Name {
			case ical.CompTimezone:
				id := ""
				if p := child.Props.Get(ical.PropTimezoneID); p != nil {
					id = p.Value
				}
				if !zones[id] {
					zones[id] = true
					timezones = append(timezones, child)
				}
			case ical.CompEvent:
				events = append(events, child)
			}
		}
	}
	if len(events) == 0 {
		// The encoder refuses a calendar without components: write it by hand.
		name := strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`).Replace(c.Name)
		return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:" + cal.ProdID + "\r\nX-WR-CALNAME:" + name + "\r\nEND:VCALENDAR\r\n", nil
	}
	out.Children = append(timezones, events...)
	return cal.Encode(out)
}
