package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bhr-tools/calendar/internal/db"
)

// Connecting an app (the Assistant…) to Calendar, in the manner of OAuth,
// the same way as the user's other apps:
//
//  1. the app sends the user to /?connect=<name>&redirect_uri=<url>&state=<s>;
//  2. Calendar asks the user, logged in, which calendar the app may read;
//  3. on Allow, the page gets a one-time code (POST /api/connect) and goes
//     back to redirect_uri?code=…&state=…;
//  4. the app's server trades the code for a lasting token
//     (POST /api/connect/token), sent to it only, never to the browser.
//
// An app token says what it opens (/api/app/info) and reads the events of
// its calendar (/api/app/events), nothing else. The user disconnects the
// app from Settings → Connected apps (or by deleting the calendar); the app
// may leave (DELETE /api/connect).

const codeTTL = 5 * time.Minute

type connectCode struct {
	app, calendar, redirect string
	expires                 time.Time
}

type connectCodes struct {
	mu    sync.Mutex
	codes map[string]connectCode
}

// validRedirect accepts an absolute http(s) address.
func validRedirect(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.Fragment == ""
}

// connect gives the logged-in user's page a one-time code for an app, bound
// to the calendar chosen.
func (a *API) connect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		App         string `json:"app"`
		RedirectURI string `json:"redirect_uri"`
		CalendarID  string `json:"calendar_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.App = strings.TrimSpace(req.App)
	if req.App == "" || len(req.App) > 60 {
		writeError(w, http.StatusBadRequest, "The app needs a name of at most 60 characters")
		return
	}
	if !validRedirect(req.RedirectURI) {
		writeError(w, http.StatusBadRequest, "The app gave no valid address to come back to")
		return
	}
	if _, err := a.store.GetCalendar(req.CalendarID); err != nil {
		fail(w, r, err)
		return
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		fail(w, r, err)
		return
	}
	code := hex.EncodeToString(b)
	now := time.Now()
	a.codes.mu.Lock()
	if a.codes.codes == nil {
		a.codes.codes = map[string]connectCode{}
	}
	for c, p := range a.codes.codes {
		if now.After(p.expires) {
			delete(a.codes.codes, c)
		}
	}
	a.codes.codes[code] = connectCode{app: req.App, calendar: req.CalendarID, redirect: req.RedirectURI, expires: now.Add(codeTTL)}
	a.codes.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"code": code})
}

// connectToken trades a one-time code for an app token.
func (a *API) connectToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code        string `json:"code"`
		RedirectURI string `json:"redirect_uri"`
	}
	if !decode(w, r, &req) {
		return
	}
	a.codes.mu.Lock()
	p, ok := a.codes.codes[req.Code]
	delete(a.codes.codes, req.Code) // one use, right or wrong
	a.codes.mu.Unlock()
	if !ok || time.Now().After(p.expires) || p.redirect != req.RedirectURI {
		writeError(w, http.StatusBadRequest, "Unknown or expired code: connect again")
		return
	}
	token, err := a.store.CreateAppToken(p.app, p.calendar)
	if err != nil {
		fail(w, r, err)
		return
	}
	c, err := a.store.GetCalendar(p.calendar)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token, "id": c.ID, "name": c.Name})
}

func bearer(r *http.Request) string {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return tok
}

// disconnectSelf lets an app give its token back.
func (a *API) disconnectSelf(w http.ResponseWriter, r *http.Request) {
	if bearer(r) == "" {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if err := a.store.DeleteAppToken(bearer(r)); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) listApps(w http.ResponseWriter, r *http.Request) {
	apps, err := a.store.ListApps()
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, apps)
}

func (a *API) deleteApp(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Unknown app")
		return
	}
	if err := a.store.DeleteApp(id); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// appCalendar passes the calendar of a valid app token to next.
func (a *API) appCalendar(next func(w http.ResponseWriter, r *http.Request, c *db.Calendar)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := a.store.AppCalendar(bearer(r))
		if !ok {
			writeError(w, http.StatusUnauthorized, "This app is not connected (any more): connect it again")
			return
		}
		c, err := a.store.GetCalendar(id)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "This app is not connected (any more): connect it again")
			return
		}
		next(w, r, c)
	}
}

// appInfo is the same in each of the user's apps: which app answers, and
// what the token opens (here: one calendar, by id and name).
func (a *API) appInfo(w http.ResponseWriter, r *http.Request, c *db.Calendar) {
	writeJSON(w, http.StatusOK, map[string]string{"app": "Calendar", "scope": "calendar", "id": c.ID, "name": c.Name})
}

// appEvent is what an app sees of an occurrence: all-day events have days
// ("YYYY-MM-DD", end exclusive), others RFC 3339 times.
type appEvent struct {
	UID         string `json:"uid"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	AllDay      bool   `json:"all_day"`
	Start       string `json:"start"`
	End         string `json:"end"`
}

// appEvents lists the occurrences of the app's calendar between two days
// (from included, to excluded; default: from today, for a year).
func (a *API) appEvents(w http.ResponseWriter, r *http.Request, c *db.Calendar) {
	q := r.URL.Query()
	today := time.Now().In(a.loc)
	from := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, a.loc)
	to := from.AddDate(1, 0, 0)
	var err1, err2 error
	if s := q.Get("from"); s != "" {
		from, err1 = time.ParseInLocation(dateLayout, s, a.loc)
	}
	if s := q.Get("to"); s != "" {
		to, err2 = time.ParseInLocation(dateLayout, s, a.loc)
	}
	if err1 != nil || err2 != nil || !to.After(from) || to.Sub(from) > 400*24*time.Hour {
		writeError(w, http.StatusBadRequest, "Expected from and to days (YYYY-MM-DD), at most a year apart")
		return
	}
	list, err := a.events.Between([]db.Calendar{*c}, from, to)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := []appEvent{}
	for _, o := range list {
		if o.Cancelled {
			continue
		}
		e := toJSON(o)
		out = append(out, appEvent{UID: e.UID, Title: e.Title, Description: e.Notes, AllDay: e.AllDay, Start: e.Start, End: e.End})
	}
	writeJSON(w, http.StatusOK, out)
}
