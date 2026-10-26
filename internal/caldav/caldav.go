// Package caldav serves calendars over CalDAV (RFC 4791), so that the
// calendar apps of phones and desktops (Apple Calendar, DAVx5, Thunderbird…)
// read and write the same events as the web app, live.
//
// Layout, under Prefix (/dav):
//
//	/                      root, points to the principal
//	/principal/            the single user, points to the calendar home
//	/calendars/            calendar home, lists the calendars
//	/calendars/<id>/       a calendar (GET exports it as one .ics file)
//	/calendars/<id>/<name> an event (one iCalendar object)
//
// Change detection uses the calendar's sync token, exposed both as a ctag
// (CalendarServer extension) and through sync-collection (RFC 6578).
package caldav

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bhr-tools/calendar/internal/db"
	"github.com/bhr-tools/calendar/internal/events"
)

const (
	Prefix        = "/dav"
	maxObjectSize = 10 << 20 // 10 MiB
	maxXMLBody    = 1 << 20
	syncPrefix    = "https://bhr-tools.github.io/calendar/sync/"
	authCacheTTL  = 15 * time.Minute
	failedDelay   = time.Second
)

// Colors maps the palette to hex, for clients (Rosé Pine main variant).
var Colors = map[string]string{
	"pine": "#31748F", "foam": "#9CCFD8", "iris": "#C4A7E7",
	"rose": "#EBBCBA", "love": "#EB6F92", "gold": "#F6C177",
}

// Handler serves CalDAV requests.
type Handler struct {
	Store  *db.Store
	Events *events.Service
	NoAuth bool

	authMu    sync.Mutex
	authKey   [32]byte // sha256 of the last accepted password
	authStamp string   // password it was checked against
	authUntil time.Time
}

// ServeHTTP authenticates the request, then routes it by method.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("DAV", "1, 3, calendar-access")
	if r.Method == http.MethodOptions {
		w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, PROPPATCH, REPORT, MKCALENDAR, MOVE")
		w.WriteHeader(http.StatusOK)
		return
	}
	if !h.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Calendar", charset="UTF-8"`)
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}
	res := parsePath(r.URL.Path)
	var err error
	switch r.Method {
	case "PROPFIND":
		err = h.propfind(w, r, res)
	case "PROPPATCH":
		err = h.proppatch(w, r, res)
	case "REPORT":
		err = h.report(w, r, res)
	case "MKCALENDAR", "MKCOL":
		err = h.mkcalendar(w, r, res)
	case http.MethodGet, http.MethodHead:
		err = h.get(w, r, res)
	case http.MethodPut:
		err = h.put(w, r, res)
	case http.MethodDelete:
		err = h.delete(w, r, res)
	case "MOVE":
		err = h.move(w, r, res)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err != nil {
		h.fail(w, r, err)
	}
}

// authorized checks HTTP Basic credentials against the master password. The
// user name is ignored. A successful check is cached: clients authenticate
// every request, and hashing the password each time would be slow.
func (h *Handler) authorized(r *http.Request) bool {
	if h.NoAuth {
		return true
	}
	_, password, ok := r.BasicAuth()
	if !ok {
		return false
	}
	stamp, err := h.Store.PasswordStamp()
	if err != nil || stamp == "" {
		// No password yet: set one in the web app first.
		return false
	}
	key := sha256.Sum256([]byte(password))

	h.authMu.Lock()
	defer h.authMu.Unlock()
	if h.authStamp == stamp && time.Now().Before(h.authUntil) && subtle.ConstantTimeCompare(key[:], h.authKey[:]) == 1 {
		return true
	}
	ok, err = h.Store.VerifyPassword(password)
	if err != nil || !ok {
		time.Sleep(failedDelay)
		return false
	}
	h.authKey, h.authStamp, h.authUntil = key, stamp, time.Now().Add(authCacheTTL)
	return true
}

type kind int

const (
	kindUnknown kind = iota
	kindRoot
	kindPrincipal
	kindHome
	kindCalendar
	kindObject
)

type resource struct {
	kind     kind
	calendar string
	object   string
}

// parsePath maps a request path (decoded) to a resource. The site root "/"
// is the DAV root too, for clients that only know the server name.
func parsePath(p string) resource {
	p = strings.TrimPrefix(p, Prefix)
	parts := strings.FieldsFunc(p, func(r rune) bool { return r == '/' })
	switch {
	case len(parts) == 0:
		return resource{kind: kindRoot}
	case parts[0] == "principal" && len(parts) == 1:
		return resource{kind: kindPrincipal}
	case parts[0] == "calendars" && len(parts) == 1:
		return resource{kind: kindHome}
	case parts[0] == "calendars" && len(parts) == 2:
		return resource{kind: kindCalendar, calendar: parts[1]}
	case parts[0] == "calendars" && len(parts) == 3:
		return resource{kind: kindObject, calendar: parts[1], object: parts[2]}
	}
	return resource{kind: kindUnknown}
}

func hrefCalendar(id string) string {
	return Prefix + "/calendars/" + url.PathEscape(id) + "/"
}

func hrefObject(calendarID, name string) string {
	return hrefCalendar(calendarID) + url.PathEscape(name)
}

func (res resource) href() string {
	switch res.kind {
	case kindRoot:
		return Prefix + "/"
	case kindPrincipal:
		return Prefix + "/principal/"
	case kindHome:
		return Prefix + "/calendars/"
	case kindCalendar:
		return hrefCalendar(res.calendar)
	case kindObject:
		return hrefObject(res.calendar, res.object)
	}
	return ""
}

// hrefPath turns an href from a request body (absolute URL or path,
// percent-encoded) into a resource.
func hrefPath(href string) resource {
	href = strings.TrimSpace(href)
	if u, err := url.Parse(href); err == nil {
		return parsePath(u.Path)
	}
	return resource{}
}

func quote(etag string) string {
	return `"` + etag + `"`
}

func unquote(etag string) string {
	etag = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(etag), "W/"))
	return strings.Trim(etag, `"`)
}

func syncToken(token int64) string {
	return syncPrefix + strconv.FormatInt(token, 10)
}

func parseSyncToken(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(s[strings.LastIndex(s, "/")+1:], 10, 64)
	return n, err == nil && n >= 0
}

// davError answers with a WebDAV error body naming a failed precondition.
func davError(w http.ResponseWriter, status int, condition string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?>`+"\n"+`<d:error xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">%s</d:error>`, condition)
}

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var he *httpError
	var conflict *db.UIDConflictError
	var invalidInput *db.ValidationError
	switch {
	case errors.As(err, &he):
		http.Error(w, he.msg, he.status)
	case errors.Is(err, db.ErrNotFound):
		http.Error(w, "Not found", http.StatusNotFound)
	case errors.Is(err, db.ErrPrecondition):
		http.Error(w, "Precondition failed", http.StatusPreconditionFailed)
	case errors.Is(err, db.ErrReadOnly):
		davError(w, http.StatusForbidden, "<d:need-privileges/>")
	case errors.As(err, &conflict):
		davError(w, http.StatusForbidden, "<c:no-uid-conflict><d:href>"+escape(hrefObject(parsePath(r.URL.Path).calendar, conflict.Name))+"</d:href></c:no-uid-conflict>")
	case events.IsInvalid(err):
		log.Printf("caldav: %s %s: %v", r.Method, r.URL.Path, err)
		davError(w, http.StatusForbidden, "<c:valid-calendar-data/>")
	case errors.As(err, &invalidInput):
		http.Error(w, invalidInput.Msg, http.StatusBadRequest)
	default:
		log.Printf("caldav: %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func readBody(r *http.Request) (*node, error) {
	root, err := parseXML(io.LimitReader(r.Body, maxXMLBody))
	if err != nil {
		return nil, &httpError{http.StatusBadRequest, "invalid XML body"}
	}
	return root, nil
}

func writeMultistatus(w http.ResponseWriter, m *multistatus) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = io.WriteString(w, m.String())
}

// ---------------------------------------------------------------------------
// GET, PUT, DELETE, MOVE
// ---------------------------------------------------------------------------

func (h *Handler) get(w http.ResponseWriter, r *http.Request, res resource) error {
	switch res.kind {
	case kindObject:
		o, err := h.Store.GetObject(res.calendar, res.object)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		w.Header().Set("ETag", quote(o.ETag))
		if t, err := time.Parse(time.RFC3339, o.ModifiedAt); err == nil {
			w.Header().Set("Last-Modified", t.UTC().Format(http.TimeFormat))
		}
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(o.Data)))
			return nil
		}
		_, err = io.WriteString(w, o.Data)
		return err
	case kindCalendar:
		c, err := h.Store.GetCalendar(res.calendar)
		if err != nil {
			return err
		}
		data, err := events.Export(h.Store, c)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		w.Header().Set("ETag", quote("ctag-"+strconv.FormatInt(c.SyncToken, 10)))
		if r.Method == http.MethodHead {
			return nil
		}
		_, err = io.WriteString(w, data)
		return err
	case kindRoot, kindPrincipal, kindHome:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, err := io.WriteString(w, "CalDAV server. Add this address to your calendar app.\n")
		return err
	}
	return db.ErrNotFound
}

func conditionOf(r *http.Request) db.Condition {
	var cond db.Condition
	if v := r.Header.Get("If-Match"); v != "" {
		cond.IfMatch = unquote(v)
		if strings.TrimSpace(v) == "*" {
			cond.IfMatch = "*"
		}
	}
	if strings.TrimSpace(r.Header.Get("If-None-Match")) == "*" {
		cond.IfNoneMatch = true
	}
	return cond
}

func (h *Handler) put(w http.ResponseWriter, r *http.Request, res resource) error {
	if res.kind != kindObject {
		return &httpError{http.StatusMethodNotAllowed, "PUT is only allowed on events"}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxObjectSize+1))
	if err != nil {
		return &httpError{http.StatusBadRequest, "cannot read the body"}
	}
	if len(body) > maxObjectSize {
		davError(w, http.StatusRequestEntityTooLarge, "<c:max-resource-size/>")
		return nil
	}
	o, created, err := h.Events.Put(res.calendar, res.object, string(body), conditionOf(r))
	if err != nil {
		return err
	}
	// The data is stored as sent, so the client may keep its copy: give it the ETag.
	w.Header().Set("ETag", quote(o.ETag))
	if created {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
	return nil
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request, res resource) error {
	switch res.kind {
	case kindObject:
		if err := h.Store.DeleteObject(res.calendar, res.object, conditionOf(r)); err != nil {
			return err
		}
	case kindCalendar:
		if err := h.Store.DeleteCalendar(res.calendar); err != nil {
			return err
		}
	default:
		return &httpError{http.StatusForbidden, "cannot delete this resource"}
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) move(w http.ResponseWriter, r *http.Request, res resource) error {
	if res.kind != kindObject {
		return &httpError{http.StatusForbidden, "only events can be moved"}
	}
	dest := hrefPath(r.Header.Get("Destination"))
	if dest.kind != kindObject {
		return &httpError{http.StatusBadRequest, "invalid Destination"}
	}
	if _, err := h.Store.MoveObject(res.calendar, res.object, dest.calendar, dest.object, conditionOf(r)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusCreated)
	return nil
}

// ---------------------------------------------------------------------------
// Collections: MKCALENDAR, PROPPATCH
// ---------------------------------------------------------------------------

func (h *Handler) mkcalendar(w http.ResponseWriter, r *http.Request, res resource) error {
	if res.kind != kindCalendar {
		return &httpError{http.StatusForbidden, "calendars can only be created in the calendar home"}
	}
	root, err := readBody(r)
	if err != nil {
		return err
	}
	in := db.CalendarInput{ID: res.calendar, Name: "Calendar"}
	if prop := root.find(nsDAV, "prop"); prop != nil {
		if n := prop.child(nsDAV, "displayname"); n != nil && strings.TrimSpace(n.text) != "" {
			in.Name = n.text
		}
		if n := prop.child(nsApple, "calendar-color"); n != nil {
			in.Color = nearestColor(n.text)
		}
		if set := prop.child(nsCalDAV, "supported-calendar-component-set"); set != nil {
			for _, comp := range set.children {
				if !strings.EqualFold(comp.attr("name"), "VEVENT") {
					// Task lists (VTODO) are not supported.
					davError(w, http.StatusForbidden, "<c:supported-calendar-component/>")
					return nil
				}
			}
		}
	}
	if _, err := h.Store.GetCalendar(res.calendar); err == nil {
		return &httpError{http.StatusMethodNotAllowed, "the calendar already exists"}
	}
	if _, err := h.Store.CreateCalendar(in); err != nil {
		return err
	}
	w.WriteHeader(http.StatusCreated)
	return nil
}

// proppatch applies the calendar properties clients edit (name, color,
// order) and accepts the others without storing them.
func (h *Handler) proppatch(w http.ResponseWriter, r *http.Request, res resource) error {
	root, err := readBody(r)
	if err != nil {
		return err
	}
	if root == nil {
		return &httpError{http.StatusBadRequest, "missing body"}
	}
	var results []propResult
	var up db.CalendarUpdate
	for _, op := range root.children {
		prop := op.child(nsDAV, "prop")
		if prop == nil {
			continue
		}
		for _, p := range prop.children {
			results = append(results, propResult{name: p.name, found: true})
			if res.kind != kindCalendar || !op.is(nsDAV, "set") {
				continue
			}
			switch {
			case p.is(nsDAV, "displayname") && strings.TrimSpace(p.text) != "":
				v := p.text
				up.Name = &v
			case p.is(nsApple, "calendar-color"):
				v := nearestColor(p.text)
				up.Color = &v
			case p.is(nsApple, "calendar-order"):
				if n, err := strconv.Atoi(strings.TrimSpace(p.text)); err == nil {
					up.Position = &n
				}
			}
		}
	}
	if res.kind == kindCalendar {
		if _, err := h.Store.UpdateCalendar(res.calendar, up); err != nil {
			return err
		}
	}
	m := newMultistatus()
	m.response(res.href(), results)
	writeMultistatus(w, m)
	return nil
}

// nearestColor maps a client color (#RRGGBB or #RRGGBBAA) to the closest
// palette color.
func nearestColor(hex string) string {
	rgb := func(s string) (r, g, b float64, ok bool) {
		s = strings.TrimPrefix(strings.TrimSpace(s), "#")
		if len(s) < 6 {
			return 0, 0, 0, false
		}
		v, err := strconv.ParseUint(s[:6], 16, 32)
		if err != nil {
			return 0, 0, 0, false
		}
		return float64(v >> 16 & 0xff), float64(v >> 8 & 0xff), float64(v & 0xff), true
	}
	r, g, b, ok := rgb(hex)
	if !ok {
		return db.Colors[0]
	}
	best, bestDist := db.Colors[0], math.MaxFloat64
	for _, name := range db.Colors {
		cr, cg, cb, _ := rgb(Colors[name])
		d := (r-cr)*(r-cr) + (g-cg)*(g-cg) + (b-cb)*(b-cb)
		if d < bestDist {
			best, bestDist = name, d
		}
	}
	return best
}
