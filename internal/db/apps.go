package db

import (
	"crypto/rand"
	"encoding/hex"
)

// An app token lets another app (another app of yours…) read the events of one
// calendar the user chose; nothing else. The user grants it from the
// connect page; it never expires and survives a password change, until the
// user disconnects the app or deletes the calendar. Stored hashed, like
// sessions.

// App is a connected app, as listed to the user.
type App struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	CalendarID   string  `json:"calendar_id"`
	CalendarName string  `json:"calendar_name"`
	CreatedAt    string  `json:"created_at"`
	UsedAt       *string `json:"used_at"`
}

// CreateAppToken connects an app to a calendar and returns its token.
func (s *Store) CreateAppToken(name, calendarID string) (string, error) {
	if _, err := s.GetCalendar(calendarID); err != nil {
		return "", err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	_, err := s.db.Exec(`INSERT INTO app_tokens (name, calendar_id, token_hash, created_at) VALUES (?, ?, ?, ?)`,
		name, calendarID, tokenHash(token), now())
	if err != nil {
		return "", err
	}
	return token, nil
}

// AppCalendar returns the calendar an app token may read, and records the use.
func (s *Store) AppCalendar(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	var cal string
	if err := s.db.QueryRow(`SELECT calendar_id FROM app_tokens WHERE token_hash = ?`, tokenHash(token)).Scan(&cal); err != nil {
		return "", false
	}
	_, _ = s.db.Exec(`UPDATE app_tokens SET used_at = ? WHERE token_hash = ?`, now(), tokenHash(token))
	return cal, true
}

// ListApps returns the connected apps, oldest first.
func (s *Store) ListApps() ([]App, error) {
	rows, err := s.db.Query(`SELECT a.id, a.name, a.calendar_id, COALESCE(c.name, ''), a.created_at, a.used_at
		FROM app_tokens a LEFT JOIN calendars c ON c.id = a.calendar_id ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	apps := []App{}
	for rows.Next() {
		var a App
		if err := rows.Scan(&a.ID, &a.Name, &a.CalendarID, &a.CalendarName, &a.CreatedAt, &a.UsedAt); err != nil {
			return nil, err
		}
		apps = append(apps, a)
	}
	return apps, rows.Err()
}

// DeleteApp disconnects an app by id.
func (s *Store) DeleteApp(id int64) error {
	_, err := s.db.Exec(`DELETE FROM app_tokens WHERE id = ?`, id)
	return err
}

// DeleteAppToken disconnects the app holding the token (the app leaving).
func (s *Store) DeleteAppToken(token string) error {
	_, err := s.db.Exec(`DELETE FROM app_tokens WHERE token_hash = ?`, tokenHash(token))
	return err
}
