// Command server runs Calendar: the JSON API, the CalDAV server and the
// embedded web app.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // time zones without relying on the system database

	"github.com/bhr-tools/calendar/internal/api"
	"github.com/bhr-tools/calendar/internal/caldav"
	"github.com/bhr-tools/calendar/internal/db"
	"github.com/bhr-tools/calendar/internal/events"
	"github.com/bhr-tools/calendar/internal/feeds"
	"github.com/bhr-tools/calendar/web"
)

// Each flag defaults to its environment variable, so the binary is configured
// with flags (NixOS module) or environment (container) alike.
func main() {
	host := flag.String("host", envOr("HOST", "127.0.0.1"), "listen address (env HOST)")
	port := flag.Int("port", envInt("PORT", 8080), "listen port (env PORT)")
	dataDir := flag.String("data-dir", envOr("DATA_DIR", "./data"), "directory of the database (env DATA_DIR)")
	dbPath := flag.String("db", os.Getenv("DB_PATH"), "database file, overrides -data-dir (env DB_PATH)")
	noAuth := flag.Bool("no-auth", envBool("NO_AUTH"), "disable the master password (env NO_AUTH=true)")
	timezone := flag.String("timezone", envOr("TIMEZONE", "Europe/Paris"), "time zone of events without one (env TIMEZONE)")
	refresh := flag.Duration("refresh", envDuration("REFRESH", 30*time.Minute), "interval between subscription downloads (env REFRESH)")
	flag.Parse()

	if *port < 1 || *port > 65535 {
		log.Fatalf("invalid port %d", *port)
	}
	loc, err := time.LoadLocation(*timezone)
	if err != nil {
		log.Fatalf("invalid time zone %q: %v", *timezone, err)
	}
	if *refresh < time.Minute {
		log.Fatalf("invalid refresh interval %s: at least 1m", *refresh)
	}
	path := *dbPath
	if path == "" {
		path = filepath.Join(*dataDir, "calendar.db")
	}

	store, err := db.Open(path)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer store.Close()
	if err := ensureCalendar(store); err != nil {
		log.Fatalf("database: %v", err)
	}

	svc := &events.Service{Store: store, Loc: loc}
	refresher := &feeds.Refresher{Store: store, Loc: loc, Interval: *refresh}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go refresher.Run(ctx)

	addr := net.JoinHostPort(*host, strconv.Itoa(*port))
	server := &http.Server{
		Addr:              addr,
		Handler:           newHandler(store, svc, refresher, *noAuth),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("Calendar listening on http://%s (database %s, time zone %s)", addr, path, loc)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutting down")
	cancel()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// ensureCalendar creates a first calendar on a new database, so events can be
// added right away.
func ensureCalendar(store *db.Store) error {
	list, err := store.ListCalendars()
	if err != nil || len(list) > 0 {
		return err
	}
	_, err = store.CreateCalendar(db.CalendarInput{Name: "Personal", Color: "pine"})
	return err
}

// davMethods are the WebDAV methods clients may send to the site root before
// discovering /dav/.
var davMethods = map[string]bool{"PROPFIND": true, "REPORT": true, "PROPPATCH": true, "MKCALENDAR": true, http.MethodOptions: true}

// newHandler routes the API, CalDAV, the health check and the web app.
func newHandler(store *db.Store, svc *events.Service, refresher *feeds.Refresher, noAuth bool) http.Handler {
	dav := &caldav.Handler{Store: store, Events: svc, NoAuth: noAuth}
	app := noCache(http.FileServerFS(web.Files))

	mux := http.NewServeMux()
	a := api.New(store, svc, refresher, noAuth)
	mux.Handle("/api/", a.Handler())
	mux.HandleFunc("GET "+api.FeedPrefix, a.Feed)
	mux.Handle(caldav.Prefix+"/", dav)
	mux.Handle(caldav.Prefix, http.RedirectHandler(caldav.Prefix+"/", http.StatusMovedPermanently))
	// RFC 6764 discovery.
	mux.Handle("/.well-known/caldav", http.RedirectHandler(caldav.Prefix+"/", http.StatusMovedPermanently))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := store.Ping(); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && davMethods[r.Method] {
			dav.ServeHTTP(w, r)
			return
		}
		app.ServeHTTP(w, r)
	})
	return securityHeaders(mux)
}

// noCache makes browsers revalidate the web app on every load, so an upgrade
// never mixes old and new JavaScript modules.
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; manifest-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Fatalf("invalid %s=%q: not a number", key, v)
	}
	return n
}

func envBool(key string) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		log.Fatalf("invalid %s=%q: expected true or false", key, v)
	}
	return b
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Fatalf("invalid %s=%q: expected a duration such as 30m", key, v)
	}
	return d
}
