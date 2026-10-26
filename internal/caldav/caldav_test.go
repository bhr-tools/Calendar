package caldav

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bhr-tools/calendar/internal/db"
	"github.com/bhr-tools/calendar/internal/events"
)

const password = "correct horse"

type client struct {
	t   *testing.T
	srv *httptest.Server
}

func setup(t *testing.T) (*client, *db.Store, string) {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.SetPassword(password); err != nil {
		t.Fatal(err)
	}
	c, err := store.CreateCalendar(db.CalendarInput{Name: "Personal", Color: "pine"})
	if err != nil {
		t.Fatal(err)
	}
	paris, _ := time.LoadLocation("Europe/Paris")
	h := &Handler{Store: store, Events: &events.Service{Store: store, Loc: paris}}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &client{t: t, srv: srv}, store, c.ID
}

func (c *client) do(method, path, body string, headers ...string) (*http.Response, string) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.srv.URL+path, strings.NewReader(body))
	req.SetBasicAuth("me", password)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return res, string(data)
}

func expect(t *testing.T, res *http.Response, body string, status int) {
	t.Helper()
	if res.StatusCode != status {
		t.Fatalf("%s %s: status %d, want %d\n%s", res.Request.Method, res.Request.URL.Path, res.StatusCode, status, body)
	}
}

const event = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Apple Inc.//iOS 19//EN\r\n" +
	"BEGIN:VEVENT\r\nUID:ios-1\r\nDTSTAMP:20261026T100000Z\r\nDTSTART;TZID=Europe/Paris:20261027T090000\r\n" +
	"DTEND;TZID=Europe/Paris:20261027T100000\r\nSUMMARY:Deep work\r\nX-APPLE-TRAVEL-ADVISORY-BEHAVIOR:AUTOMATIC\r\n" +
	"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nDESCRIPTION:Reminder\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

var tokenRE = regexp.MustCompile(`<d:sync-token>([^<]+)</d:sync-token>`)

func TestAppleLikeSession(t *testing.T) {
	c, _, id := setup(t)
	cal := "/dav/calendars/" + id + "/"

	// Unauthenticated requests are refused.
	req, _ := http.NewRequest("PROPFIND", c.srv.URL+"/dav/", nil)
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous PROPFIND: %d", res.StatusCode)
	}

	// Discovery: principal, then home, then calendars.
	res, body := c.do("PROPFIND", "/dav/", `<d:propfind xmlns:d="DAV:"><d:prop><d:current-user-principal/></d:prop></d:propfind>`, "Depth", "0")
	expect(t, res, body, 207)
	if !strings.Contains(body, "<d:href>/dav/principal/</d:href>") {
		t.Fatalf("no principal:\n%s", body)
	}
	res, body = c.do("PROPFIND", "/dav/principal/", `<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><c:calendar-home-set/></d:prop></d:propfind>`, "Depth", "0")
	expect(t, res, body, 207)
	if !strings.Contains(body, "<c:calendar-home-set><d:href>/dav/calendars/</d:href>") {
		t.Fatalf("no home:\n%s", body)
	}

	// Initial sync: empty calendar.
	syncBody := func(token string) string {
		return `<d:sync-collection xmlns:d="DAV:"><d:sync-token>` + token + `</d:sync-token><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>`
	}
	res, body = c.do("REPORT", cal, syncBody(""))
	expect(t, res, body, 207)
	token := tokenRE.FindStringSubmatch(body)[1]

	// Create, with If-None-Match, then the same again must fail.
	res, body = c.do("PUT", cal+"ios-1.ics", event, "If-None-Match", "*", "Content-Type", "text/calendar")
	expect(t, res, body, 201)
	etag := res.Header.Get("ETag")
	res, body = c.do("PUT", cal+"ios-1.ics", event, "If-None-Match", "*")
	expect(t, res, body, 412)

	// The data comes back byte for byte, with the same ETag.
	res, body = c.do("GET", cal+"ios-1.ics", "")
	expect(t, res, body, 200)
	if body != event || res.Header.Get("ETag") != etag {
		t.Fatalf("GET changed the object (etag %s vs %s)", res.Header.Get("ETag"), etag)
	}

	// Incremental sync sees the new event; multiget returns its data.
	res, body = c.do("REPORT", cal, syncBody(token))
	expect(t, res, body, 207)
	if !strings.Contains(body, "/ios-1.ics</d:href>") || !strings.Contains(body, etag) {
		t.Fatalf("sync does not list the event:\n%s", body)
	}
	token = tokenRE.FindStringSubmatch(body)[1]
	res, body = c.do("REPORT", cal, `<c:calendar-multiget xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><d:getetag/><c:calendar-data/></d:prop><d:href>`+cal+`ios-1.ics</d:href><d:href>`+cal+`missing.ics</d:href></c:calendar-multiget>`)
	expect(t, res, body, 207)
	if !strings.Contains(body, "SUMMARY:Deep work") || !strings.Contains(body, "404 Not Found") {
		t.Fatalf("multiget:\n%s", body)
	}

	// The ctag moved.
	res, body = c.do("PROPFIND", cal, `<d:propfind xmlns:d="DAV:" xmlns:cs="http://calendarserver.org/ns/"><d:prop><cs:getctag/></d:prop></d:propfind>`, "Depth", "0")
	if !strings.Contains(body, "<cs:getctag>1</cs:getctag>") {
		t.Fatalf("ctag:\n%s", body)
	}

	// Time-range query.
	res, body = c.do("REPORT", cal, `<c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><d:getetag/></d:prop><c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT"><c:time-range start="20261027T000000Z" end="20261028T000000Z"/></c:comp-filter></c:comp-filter></c:filter></c:calendar-query>`, "Depth", "1")
	if !strings.Contains(body, "ios-1.ics") {
		t.Fatalf("query in range:\n%s", body)
	}
	res, body = c.do("REPORT", cal, `<c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><d:getetag/></d:prop><c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT"><c:time-range start="20261101T000000Z" end="20261102T000000Z"/></c:comp-filter></c:comp-filter></c:filter></c:calendar-query>`, "Depth", "1")
	if strings.Contains(body, "ios-1.ics") {
		t.Fatalf("query out of range:\n%s", body)
	}

	// Stale If-Match is refused; the right one deletes.
	res, body = c.do("DELETE", cal+"ios-1.ics", "", "If-Match", `"stale"`)
	expect(t, res, body, 412)
	res, body = c.do("DELETE", cal+"ios-1.ics", "", "If-Match", etag)
	expect(t, res, body, 204)
	res, body = c.do("REPORT", cal, syncBody(token))
	if !strings.Contains(body, "/ios-1.ics</d:href><d:status>HTTP/1.1 404 Not Found") {
		t.Fatalf("sync does not report the deletion:\n%s", body)
	}

	// Invalid data and tasks are refused.
	res, body = c.do("PUT", cal+"bad.ics", "not ical")
	expect(t, res, body, 403)
	todo := strings.ReplaceAll(strings.ReplaceAll(event, "VEVENT", "VTODO"), "ios-1", "todo-1")
	res, body = c.do("PUT", cal+"todo.ics", todo)
	expect(t, res, body, 403)

	// Invalid sync token.
	res, body = c.do("REPORT", cal, syncBody("https://bhr-tools.github.io/calendar/sync/999"))
	expect(t, res, body, 403)
}

func TestCalendarsFromClient(t *testing.T) {
	c, store, _ := setup(t)
	res, body := c.do("MKCALENDAR", "/dav/calendars/work/", `<c:mkcalendar xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:a="http://apple.com/ns/ical/"><d:set><d:prop><d:displayname>Work</d:displayname><a:calendar-color>#EB6F92FF</a:calendar-color></d:prop></d:set></c:mkcalendar>`)
	expect(t, res, body, 201)
	got, err := store.GetCalendar("work")
	if err != nil || got.Name != "Work" || got.Color != "love" {
		t.Fatalf("created %+v, %v", got, err)
	}
	res, body = c.do("PROPPATCH", "/dav/calendars/work/", `<d:propertyupdate xmlns:d="DAV:" xmlns:a="http://apple.com/ns/ical/"><d:set><d:prop><d:displayname>Job</d:displayname><a:calendar-color>#9CCFD8</a:calendar-color></d:prop></d:set></d:propertyupdate>`)
	expect(t, res, body, 207)
	got, _ = store.GetCalendar("work")
	if got.Name != "Job" || got.Color != "foam" {
		t.Fatalf("after PROPPATCH: %+v", got)
	}
	res, body = c.do("DELETE", "/dav/calendars/work/", "")
	expect(t, res, body, 204)
}

func TestSubscriptionsAreReadOnly(t *testing.T) {
	c, store, _ := setup(t)
	sub, err := store.CreateCalendar(db.CalendarInput{Name: "School", SourceURL: "https://example.com/feed.ics"})
	if err != nil {
		t.Fatal(err)
	}
	res, body := c.do("PUT", "/dav/calendars/"+sub.ID+"/x.ics", event)
	expect(t, res, body, 403)
	res, body = c.do("PROPFIND", "/dav/calendars/"+sub.ID+"/", `<d:propfind xmlns:d="DAV:"><d:prop><d:current-user-privilege-set/></d:prop></d:propfind>`, "Depth", "0")
	if strings.Contains(body, "<d:write/>") {
		t.Fatalf("subscription advertises write access:\n%s", body)
	}
}
