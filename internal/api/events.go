package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/bhr-tools/calendar/internal/cal"
	"github.com/bhr-tools/calendar/internal/db"
	"github.com/bhr-tools/calendar/internal/events"
)

// eventJSON is an occurrence as the web app sees it. Times are RFC 3339, or
// "YYYY-MM-DD" for all-day events (end exclusive).
type eventJSON struct {
	CalendarID   string `json:"calendar_id"`
	Name         string `json:"name"`
	UID          string `json:"uid"`
	Title        string `json:"title"`
	Location     string `json:"location,omitempty"`
	Notes        string `json:"notes,omitempty"`
	AllDay       bool   `json:"all_day"`
	Start        string `json:"start"`
	End          string `json:"end"`
	Recurring    bool   `json:"recurring,omitempty"`
	RecurrenceID string `json:"recurrence_id,omitempty"`
	RRule        string `json:"rrule,omitempty"`
	Cancelled    bool   `json:"cancelled,omitempty"`
	ReadOnly     bool   `json:"read_only,omitempty"`
}

const dateLayout = "2006-01-02"

func toJSON(o events.Occurrence) eventJSON {
	e := eventJSON{
		CalendarID: o.CalendarID,
		Name:       o.Name,
		UID:        o.UID,
		Title:      o.Title,
		Location:   o.Location,
		Notes:      o.Notes,
		AllDay:     o.AllDay,
		Recurring:  o.Recurring,
		RRule:      o.RRule,
		Cancelled:  o.Cancelled,
		ReadOnly:   o.ReadOnly,
	}
	if o.AllDay {
		e.Start, e.End = o.Start.Format(dateLayout), o.End.Format(dateLayout)
	} else {
		e.Start, e.End = o.Start.Format(time.RFC3339), o.End.Format(time.RFC3339)
	}
	if o.Recurring {
		e.RecurrenceID = o.RecurrenceID.Format(time.RFC3339)
	}
	return e
}

func toJSONList(list []events.Occurrence) []eventJSON {
	out := make([]eventJSON, len(list))
	for i, o := range list {
		out[i] = toJSON(o)
	}
	return out
}

// listEvents returns the occurrences between two days (to exclusive) of the
// browser's time zone.
func (a *API) listEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	loc := a.zone(q.Get("tz"))
	from, err1 := time.ParseInLocation(dateLayout, q.Get("from"), loc)
	to, err2 := time.ParseInLocation(dateLayout, q.Get("to"), loc)
	if err1 != nil || err2 != nil || !to.After(from) || to.Sub(from) > 400*24*time.Hour {
		writeError(w, http.StatusBadRequest, "Expected from and to days (YYYY-MM-DD), at most a year apart")
		return
	}
	calendars, err := a.store.ListCalendars()
	if err != nil {
		fail(w, r, err)
		return
	}
	list, err := a.events.Between(calendars, from, to)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toJSONList(list))
}

type eventRequest struct {
	CalendarID   string  `json:"calendar_id"`
	Title        string  `json:"title"`
	Location     string  `json:"location"`
	Notes        string  `json:"notes"`
	AllDay       bool    `json:"all_day"`
	Start        string  `json:"start"`
	End          string  `json:"end"`
	RRule        *string `json:"rrule"`
	TZ           string  `json:"tz"`
	Scope        string  `json:"scope"`
	RecurrenceID string  `json:"recurrence_id"`
}

// parseTime reads an RFC 3339 time, or a day ("YYYY-MM-DD", midnight in loc).
func parseTime(s string, loc *time.Location) (time.Time, bool) {
	if t, err := time.ParseInLocation(dateLayout, s, loc); err == nil {
		return t, true
	}
	// An unescaped "+01:00" in a query string arrives as " 01:00".
	t, err := time.Parse(time.RFC3339, strings.Replace(s, " ", "+", 1))
	return t, err == nil
}

// fields converts a request into event fields, answering 400 when invalid.
func (a *API) fields(w http.ResponseWriter, req eventRequest) (cal.Fields, *time.Location, bool) {
	loc := a.zone(req.TZ)
	start, ok1 := parseTime(req.Start, loc)
	end, ok2 := parseTime(req.End, loc)
	if !ok1 || !ok2 {
		writeError(w, http.StatusBadRequest, "Invalid start or end")
		return cal.Fields{}, nil, false
	}
	if req.AllDay {
		// Days only: drop any time of day the client sent.
		start = midnight(start, loc)
		end = midnight(end, loc)
		if !end.After(start) {
			end = start.AddDate(0, 0, 1)
		}
	}
	if len([]rune(req.Title)) > 500 || len(req.Notes) > 100_000 || len([]rune(req.Location)) > 500 {
		writeError(w, http.StatusBadRequest, "Text too long")
		return cal.Fields{}, nil, false
	}
	return cal.Fields{
		Title:    strings.TrimSpace(req.Title),
		Location: req.Location,
		Notes:    req.Notes,
		AllDay:   req.AllDay,
		Start:    start,
		End:      end,
		RRule:    req.RRule,
	}, loc, true
}

func midnight(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// occurrenceOf returns the first occurrence of a just written object.
func (a *API) occurrenceOf(o *db.Object, recurrenceID time.Time) (eventJSON, error) {
	c, err := a.store.GetCalendar(o.CalendarID)
	if err != nil {
		return eventJSON{}, err
	}
	parsed, err := cal.Parse(o.Data)
	if err != nil {
		return eventJSON{}, err
	}
	occ, err := cal.Find(parsed, a.loc, recurrenceID)
	if err != nil {
		// The occurrence moved away (series shifted): return the series start.
		occ, err = cal.Find(parsed, a.loc, time.Time{})
		if err != nil {
			return eventJSON{}, err
		}
	}
	return toJSON(events.Occurrence{Occurrence: occ, CalendarID: o.CalendarID, Name: o.Name, ReadOnly: c.ReadOnly()}), nil
}

func (a *API) createEvent(w http.ResponseWriter, r *http.Request) {
	var req eventRequest
	if !decode(w, r, &req) {
		return
	}
	f, loc, ok := a.fields(w, req)
	if !ok {
		return
	}
	o, err := a.events.Create(req.CalendarID, f, loc)
	if err != nil {
		fail(w, r, err)
		return
	}
	e, err := a.occurrenceOf(o, time.Time{})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

func scopeOf(s string) events.Scope {
	if s == string(events.ScopeThis) {
		return events.ScopeThis
	}
	return events.ScopeAll
}

func (a *API) updateEvent(w http.ResponseWriter, r *http.Request) {
	var req eventRequest
	if !decode(w, r, &req) {
		return
	}
	f, loc, ok := a.fields(w, req)
	if !ok {
		return
	}
	var recurrenceID time.Time
	if req.RecurrenceID != "" {
		var valid bool
		if recurrenceID, valid = parseTime(req.RecurrenceID, a.loc); !valid {
			writeError(w, http.StatusBadRequest, "Invalid recurrence_id")
			return
		}
	}
	scope := scopeOf(req.Scope)
	o, err := a.events.Update(r.PathValue("calendar"), r.PathValue("name"), recurrenceID, scope, f, req.CalendarID, loc)
	if err != nil {
		fail(w, r, err)
		return
	}
	if scope == events.ScopeAll {
		recurrenceID = time.Time{}
	}
	e, err := a.occurrenceOf(o, recurrenceID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (a *API) deleteEvent(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var recurrenceID time.Time
	if s := q.Get("recurrence_id"); s != "" {
		var ok bool
		if recurrenceID, ok = parseTime(s, a.loc); !ok {
			writeError(w, http.StatusBadRequest, "Invalid recurrence_id")
			return
		}
	}
	if err := a.events.Delete(r.PathValue("calendar"), r.PathValue("name"), recurrenceID, scopeOf(q.Get("scope"))); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) search(w http.ResponseWriter, r *http.Request) {
	calendars, err := a.store.ListCalendars()
	if err != nil {
		fail(w, r, err)
		return
	}
	list, err := a.events.Search(calendars, r.URL.Query().Get("q"), 50)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toJSONList(list))
}
