// Package api exposes calendars and events over a JSON HTTP API, for the web
// app.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/bhr-tools/calendar/internal/db"
	"github.com/bhr-tools/calendar/internal/events"
	"github.com/bhr-tools/calendar/internal/feeds"
)

const (
	maxJSONBody   = 1 << 20  // 1 MiB
	maxUploadBody = 20 << 20 // 20 MiB
	sessionCookie = "calendar_session"
)

// API holds the HTTP handlers.
type API struct {
	store  *db.Store
	events *events.Service
	feeds  *feeds.Refresher
	loc    *time.Location
	noAuth bool
	// loginMu serializes password checks; a failed attempt holds it for a
	// moment, which caps brute-force attempts without any bookkeeping.
	loginMu sync.Mutex
}

// New returns the API. With noAuth, every request is accepted without login.
func New(store *db.Store, svc *events.Service, refresher *feeds.Refresher, noAuth bool) *API {
	return &API{store: store, events: svc, feeds: refresher, loc: svc.Loc, noAuth: noAuth}
}

// Handler returns the API routes, wrapped in their middlewares.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	open := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, h) }
	auth := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, a.requireAuth(h)) }

	open("GET /api/auth/status", a.authStatus)
	open("POST /api/auth/setup", a.authSetup)
	open("POST /api/auth/login", a.authLogin)
	open("POST /api/auth/logout", a.authLogout)
	auth("POST /api/auth/change-password", a.authChangePassword)

	auth("GET /api/info", a.info)

	auth("GET /api/calendars", a.listCalendars)
	auth("POST /api/calendars", a.createCalendar)
	auth("POST /api/subscriptions", a.subscribe)
	auth("PATCH /api/calendars/{id}", a.updateCalendar)
	auth("DELETE /api/calendars/{id}", a.deleteCalendar)
	auth("POST /api/calendars/{id}/refresh", a.refreshCalendar)
	auth("GET /api/calendars/{id}/export", a.exportCalendar)
	auth("POST /api/calendars/{id}/import", a.importCalendar)

	auth("GET /api/events", a.listEvents)
	auth("POST /api/events", a.createEvent)
	auth("PATCH /api/events/{calendar}/{name}", a.updateEvent)
	auth("DELETE /api/events/{calendar}/{name}", a.deleteEvent)
	auth("GET /api/search", a.search)

	open("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "Unknown API endpoint")
	})
	return rejectCrossSite(mux)
}

// rejectCrossSite refuses state-changing requests sent by another site
// (CSRF). Browsers label every request with Sec-Fetch-Site; requests without
// it (curl, scripts) are not browser-driven and are let through.
func rejectCrossSite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeError(w, http.StatusForbidden, "Cross-site request refused")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.authenticated(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Authentication required", "auth_required": true})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authenticated reports whether r may use the API. Until a password is set,
// the instance is open (first-run setup).
func (a *API) authenticated(r *http.Request) bool {
	if a.noAuth {
		return true
	}
	hasPassword, err := a.store.HasPassword()
	if err != nil {
		return false
	}
	if !hasPassword {
		return true
	}
	c, err := r.Cookie(sessionCookie)
	return err == nil && a.store.ValidateSession(c.Value)
}

// info tells the web app how to connect other calendar apps.
func (a *API) info(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"server":     scheme + "://" + r.Host + "/",
		"caldav_url": scheme + "://" + r.Host + "/dav/",
		"no_auth":    a.noAuth,
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// fail answers with the status matching err: 404, 400 for invalid input, 409
// for a conflicting edit, 403 for read-only calendars, or 500 (logged, with a
// generic message).
func fail(w http.ResponseWriter, r *http.Request, err error) {
	var invalidInput *db.ValidationError
	var invalidEvent *events.InvalidError
	var conflict *db.UIDConflictError
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, http.StatusNotFound, "Not found")
	case errors.Is(err, db.ErrReadOnly):
		writeError(w, http.StatusForbidden, "This calendar is a read-only subscription")
	case errors.Is(err, db.ErrPrecondition):
		writeError(w, http.StatusConflict, "The event changed in the meantime, reload and try again")
	case errors.As(err, &conflict):
		writeError(w, http.StatusConflict, "An event with the same UID already exists")
	case errors.As(err, &invalidInput):
		writeError(w, http.StatusBadRequest, invalidInput.Msg)
	case errors.As(err, &invalidEvent):
		writeError(w, http.StatusBadRequest, invalidEvent.Msg)
	default:
		log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
		writeError(w, http.StatusInternalServerError, "Internal server error")
	}
}

// decode reads a JSON request body into dst. On failure it answers the
// request and returns false.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	body := http.MaxBytesReader(w, r.Body, maxJSONBody)
	if err := json.NewDecoder(body).Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			writeError(w, http.StatusRequestEntityTooLarge, "Request body too large")
		case errors.Is(err, io.EOF):
			writeError(w, http.StatusBadRequest, "Request body is empty")
		default:
			writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		}
		return false
	}
	return true
}

// zone returns the time zone named by the request's "tz" (the browser's), or
// the server's.
func (a *API) zone(name string) *time.Location {
	if name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return a.loc
}
