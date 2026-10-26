package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bhr-tools/calendar/internal/db"
	"github.com/bhr-tools/calendar/internal/events"
	"github.com/bhr-tools/calendar/internal/feeds"
)

func setup(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	c, err := store.CreateCalendar(db.CalendarInput{Name: "Personal"})
	if err != nil {
		t.Fatal(err)
	}
	paris, _ := time.LoadLocation("Europe/Paris")
	svc := &events.Service{Store: store, Loc: paris}
	srv := httptest.NewServer(New(store, svc, &feeds.Refresher{Store: store, Loc: paris}, true).Handler())
	t.Cleanup(srv.Close)
	return srv, c.ID
}

func call(t *testing.T, srv *httptest.Server, method, path string, body any, out any) int {
	t.Helper()
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, srv.URL+path, r)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		_ = json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

func list(t *testing.T, srv *httptest.Server) []eventJSON {
	t.Helper()
	var got []eventJSON
	if status := call(t, srv, "GET", "/api/events?from=2026-10-26&to=2026-11-02&tz=Europe/Paris", nil, &got); status != 200 {
		t.Fatalf("list: %d", status)
	}
	return got
}

func TestRecurringEditAndDelete(t *testing.T) {
	srv, calID := setup(t)
	rule := "FREQ=DAILY;COUNT=5"
	var created eventJSON
	status := call(t, srv, "POST", "/api/events", map[string]any{
		"calendar_id": calID, "title": "Deep work", "start": "2026-10-26T09:00:00+01:00", "end": "2026-10-26T11:00:00+01:00",
		"rrule": rule, "tz": "Europe/Paris",
	}, &created)
	if status != 201 || created.RRule != rule {
		t.Fatalf("create: %d %+v", status, created)
	}
	all := list(t, srv)
	if len(all) != 5 {
		t.Fatalf("got %d occurrences", len(all))
	}

	// Move Wednesday's occurrence only.
	wed := all[2]
	var moved eventJSON
	status = call(t, srv, "PATCH", "/api/events/"+calID+"/"+wed.Name, map[string]any{
		"title": "Deep work", "start": "2026-10-28T14:00:00+01:00", "end": "2026-10-28T16:00:00+01:00",
		"scope": "this", "recurrence_id": wed.RecurrenceID, "tz": "Europe/Paris",
	}, &moved)
	if status != 200 || moved.Start != "2026-10-28T14:00:00+01:00" {
		t.Fatalf("move this: %d %+v", status, moved)
	}
	// Rename the whole series from Friday's occurrence.
	fri := list(t, srv)[4]
	status = call(t, srv, "PATCH", "/api/events/"+calID+"/"+fri.Name, map[string]any{
		"title": "Focus", "start": fri.Start, "end": fri.End, "scope": "all", "recurrence_id": fri.RecurrenceID, "tz": "Europe/Paris",
	}, nil)
	if status != 200 {
		t.Fatalf("rename all: %d", status)
	}
	// Delete Tuesday's occurrence.
	tue := list(t, srv)[1]
	req, _ := http.NewRequest("DELETE", srv.URL+"/api/events/"+calID+"/"+tue.Name+"?scope=this&recurrence_id="+url.QueryEscape(tue.RecurrenceID), nil)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 204 {
		t.Fatalf("delete this: %v %v", err, res.StatusCode)
	}

	got := list(t, srv)
	var summary []string
	for _, e := range got {
		summary = append(summary, e.Start[5:16]+" "+e.Title)
	}
	want := []string{"10-26T09:00 Focus", "10-28T14:00 Focus", "10-29T09:00 Focus", "10-30T09:00 Focus"}
	if len(summary) != len(want) {
		t.Fatalf("got %q, want %q", summary, want)
	}
	for i := range want {
		if summary[i] != want[i] {
			t.Fatalf("got %q, want %q", summary, want)
		}
	}
}

func TestReadOnlySubscription(t *testing.T) {
	srv, _ := setup(t)
	var cals []db.Calendar
	call(t, srv, "GET", "/api/calendars", nil, &cals)
	if len(cals) != 1 {
		t.Fatalf("calendars: %+v", cals)
	}
	var e map[string]string
	status := call(t, srv, "POST", "/api/events", map[string]any{"calendar_id": "missing", "title": "x", "start": "2026-10-26", "end": "2026-10-27", "all_day": true}, &e)
	if status != 404 {
		t.Fatalf("unknown calendar: %d %v", status, e)
	}
}

func TestPublishedFeed(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c, _ := store.CreateCalendar(db.CalendarInput{Name: "Personal"})
	paris, _ := time.LoadLocation("Europe/Paris")
	svc := &events.Service{Store: store, Loc: paris}
	a := New(store, svc, &feeds.Refresher{Store: store, Loc: paris}, true)
	mux := http.NewServeMux()
	mux.Handle("/api/", a.Handler())
	mux.HandleFunc("GET "+FeedPrefix, a.Feed)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	call(t, srv, "POST", "/api/events", map[string]any{
		"calendar_id": c.ID, "title": "Deep work", "start": "2026-10-26T09:00:00+01:00", "end": "2026-10-26T11:00:00+01:00",
	}, nil)
	get := func(path string) (int, string) {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(body)
	}
	if status, _ := get(FeedPrefix + ".ics"); status != 404 {
		t.Fatalf("empty token: %d", status)
	}

	var published db.Calendar
	call(t, srv, "POST", "/api/calendars/"+c.ID+"/feed", nil, &published)
	if len(published.FeedToken) < 32 {
		t.Fatalf("token %q", published.FeedToken)
	}
	status, body := get(FeedPrefix + published.FeedToken + ".ics")
	if status != 200 || !strings.Contains(body, "SUMMARY:Deep work") {
		t.Fatalf("feed: %d %s", status, body)
	}

	// A new address replaces the old one; unpublishing removes it.
	var again db.Calendar
	call(t, srv, "POST", "/api/calendars/"+c.ID+"/feed", nil, &again)
	if status, _ := get(FeedPrefix + published.FeedToken + ".ics"); status != 404 {
		t.Fatalf("old address still served: %d", status)
	}
	call(t, srv, "DELETE", "/api/calendars/"+c.ID+"/feed", nil, nil)
	if status, _ := get(FeedPrefix + again.FeedToken + ".ics"); status != 404 {
		t.Fatalf("unpublished calendar still served: %d", status)
	}
}
