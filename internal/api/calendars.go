package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bhr-tools/calendar/internal/db"
	"github.com/bhr-tools/calendar/internal/events"
	"github.com/bhr-tools/calendar/internal/feeds"
)

func (a *API) listCalendars(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.ListCalendars()
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type calendarRequest struct {
	Name     *string `json:"name"`
	Color    *string `json:"color"`
	Hidden   *bool   `json:"hidden"`
	Position *int    `json:"position"`
	URL      string  `json:"url"`
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (a *API) createCalendar(w http.ResponseWriter, r *http.Request) {
	var req calendarRequest
	if !decode(w, r, &req) {
		return
	}
	c, err := a.store.CreateCalendar(db.CalendarInput{Name: deref(req.Name), Color: deref(req.Color)})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// subscribe adds a read-only calendar copied from an iCalendar feed, and
// downloads it once right away.
func (a *API) subscribe(w http.ResponseWriter, r *http.Request) {
	var req calendarRequest
	if !decode(w, r, &req) {
		return
	}
	link := strings.TrimSpace(req.URL)
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "webcal") || u.Host == "" {
		writeError(w, http.StatusBadRequest, "Enter the http(s) or webcal address of an iCalendar feed")
		return
	}
	name := strings.TrimSpace(deref(req.Name))
	if name == "" {
		name = u.Host
	}
	c, err := a.store.CreateCalendar(db.CalendarInput{Name: name, Color: deref(req.Color), SourceURL: link})
	if err != nil {
		fail(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	_ = a.feeds.Refresh(ctx, c.ID) // the error is recorded on the calendar
	if c, err = a.store.GetCalendar(c.ID); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (a *API) updateCalendar(w http.ResponseWriter, r *http.Request) {
	var req calendarRequest
	if !decode(w, r, &req) {
		return
	}
	c, err := a.store.UpdateCalendar(r.PathValue("id"), db.CalendarUpdate{
		Name: req.Name, Color: req.Color, Hidden: req.Hidden, Position: req.Position,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (a *API) deleteCalendar(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteCalendar(r.PathValue("id")); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) refreshCalendar(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, err := a.store.GetCalendar(id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if !c.ReadOnly() {
		writeError(w, http.StatusBadRequest, "Only subscriptions can be refreshed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	_ = a.feeds.Refresh(ctx, id)
	if c, err = a.store.GetCalendar(id); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (a *API) exportCalendar(w http.ResponseWriter, r *http.Request) {
	c, err := a.store.GetCalendar(r.PathValue("id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	data, err := events.Export(a.store, c)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"calendar.ics\"; filename*=UTF-8''%s.ics", url.PathEscape(c.Name)))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, data)
}

// publishCalendar gives a calendar a new secret address (see Feed); the
// previous address, if any, stops working.
func (a *API) publishCalendar(w http.ResponseWriter, r *http.Request) {
	c, err := a.store.SetFeedToken(r.PathValue("id"), true)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (a *API) unpublishCalendar(w http.ResponseWriter, r *http.Request) {
	c, err := a.store.SetFeedToken(r.PathValue("id"), false)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// FeedPrefix is where calendars are published: FeedPrefix + token + ".ics".
const FeedPrefix = "/ics/"

// Feed serves a published calendar as an iCalendar file, without logging
// in: the unguessable token in the address is the credential. Apps that
// subscribe to URLs but cannot log in (Google Calendar, Outlook) read it.
func (a *API) Feed(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, FeedPrefix), ".ics")
	if !ok || strings.Contains(token, "/") {
		http.NotFound(w, r)
		return
	}
	c, err := a.store.CalendarByFeedToken(token)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.NotFound(w, r)
		} else {
			fail(w, r, err)
		}
		return
	}
	data, err := events.Export(a.store, c)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Robots-Tag", "noindex")
	_, _ = io.WriteString(w, data)
}

// importCalendar adds the events of an uploaded .ics file to a calendar.
// Events already there (same UID) are replaced.
func (a *API) importCalendar(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, err := a.store.GetCalendar(id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if c.ReadOnly() {
		fail(w, r, db.ErrReadOnly)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "Choose an .ics file to import")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, "The file could not be read")
		return
	}
	objs, err := feeds.SplitImport(string(data), a.loc)
	if err != nil {
		writeError(w, http.StatusBadRequest, "This is not a valid iCalendar (.ics) file")
		return
	}
	imported, skipped := 0, 0
	for _, o := range objs {
		o.CalendarID = id
		if _, _, err := a.store.PutObject(o, db.Condition{}); err != nil {
			var conflict *db.UIDConflictError
			if !errors.As(err, &conflict) {
				fail(w, r, err)
				return
			}
			skipped++
			continue
		}
		imported++
	}
	writeJSON(w, http.StatusOK, map[string]int{"imported": imported, "skipped": skipped})
}
