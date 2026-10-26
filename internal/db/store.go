// Package db persists calendars, their iCalendar objects, the change log used
// by CalDAV sync, and authentication data in a single SQLite file.
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when the requested row does not exist.
var ErrNotFound = errors.New("not found")

// ErrPrecondition is returned when an If-Match / If-None-Match condition fails.
var ErrPrecondition = errors.New("precondition failed")

// ErrReadOnly is returned when writing to a subscribed calendar.
var ErrReadOnly = errors.New("calendar is read-only")

// ValidationError reports rejected input. Its message is meant for the user.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// Store is safe for concurrent use.
type Store struct {
	db *sql.DB
}

// querier is implemented by both *sql.DB and *sql.Tx.
type querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Open opens (creating it if needed) the database at path and migrates it.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// A single connection serializes every statement. For a personal calendar
	// this costs nothing and rules out "database is locked" errors.
	sqlDB.SetMaxOpenConns(1)

	s := &Store{db: sqlDB}
	if err := s.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Ping checks that the database answers.
func (s *Store) Ping() error {
	return s.db.Ping()
}

// tx runs fn inside a transaction, committed only if fn returns nil.
func (s *Store) tx(fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// migrations are applied in order; PRAGMA user_version records how many ran.
// Never edit a released migration: append a new one instead.
var migrations = []string{
	// 1: base schema.
	`CREATE TABLE calendars (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		color TEXT NOT NULL,
		position INTEGER NOT NULL DEFAULT 0,
		hidden INTEGER NOT NULL DEFAULT 0,
		-- Subscriptions are read-only copies of an iCalendar feed.
		source_url TEXT,
		refreshed_at TEXT,
		refresh_error TEXT,
		-- Incremented on every change of the calendar's objects (CalDAV ctag
		-- and sync token).
		sync_token INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	-- One iCalendar object per row: an event and its overridden occurrences,
	-- stored exactly as the client sent it.
	CREATE TABLE objects (
		calendar_id TEXT NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		uid TEXT NOT NULL,
		etag TEXT NOT NULL,
		data TEXT NOT NULL,
		search TEXT NOT NULL DEFAULT '',
		-- Unix seconds bounding every occurrence (last_end is MaxEnd for
		-- endless recurrences).
		first_start INTEGER NOT NULL,
		last_end INTEGER NOT NULL,
		modified_at TEXT NOT NULL,
		PRIMARY KEY (calendar_id, name)
	);
	CREATE UNIQUE INDEX idx_objects_uid ON objects(calendar_id, uid);
	CREATE INDEX idx_objects_range ON objects(calendar_id, first_start, last_end);

	-- Latest change of each object, for CalDAV sync-collection.
	CREATE TABLE changes (
		calendar_id TEXT NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		token INTEGER NOT NULL,
		deleted INTEGER NOT NULL,
		PRIMARY KEY (calendar_id, name)
	);
	CREATE INDEX idx_changes_token ON changes(calendar_id, token);

	CREATE TABLE auth_settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	CREATE TABLE auth_sessions (
		token TEXT PRIMARY KEY,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`,
}

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		err := s.tx(func(tx *sql.Tx) error {
			if _, err := tx.Exec(migrations[i]); err != nil {
				return err
			}
			_, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1))
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	return nil
}

func newID() string {
	return uuid.NewString()
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func exists(q querier, query string, args ...any) (bool, error) {
	var one int
	err := q.QueryRow(query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func nullString(ns sql.NullString) string {
	if !ns.Valid {
		return ""
	}
	return ns.String
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
