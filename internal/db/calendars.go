package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
)

// Colors are names from the Rosé Pine palette; each client maps them to the
// variant of its theme. Consecutive colors differ in hue, so new calendars
// stay apart.
var Colors = []string{"pine", "rose", "iris", "gold", "foam", "love"}

// Calendar is a collection of events. A calendar with a SourceURL is a
// read-only subscription to an iCalendar feed.
type Calendar struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Color        string `json:"color"`
	Position     int    `json:"position"`
	Hidden       bool   `json:"hidden"`
	SourceURL    string `json:"source_url,omitempty"`
	RefreshedAt  string `json:"refreshed_at,omitempty"`
	RefreshError string `json:"refresh_error,omitempty"`
	// FeedToken, when set, publishes the calendar read-only at a secret
	// address.
	FeedToken string `json:"feed_token,omitempty"`
	SyncToken int64  `json:"-"`
}

// ReadOnly reports whether the calendar is a subscription.
func (c *Calendar) ReadOnly() bool {
	return c.SourceURL != ""
}

func validColor(color string) bool {
	for _, c := range Colors {
		if c == color {
			return true
		}
	}
	return false
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", invalid("The calendar needs a name")
	}
	if len([]rune(name)) > 100 {
		return "", invalid("The calendar name is too long")
	}
	return name, nil
}

const calendarColumns = `id, name, color, position, hidden, source_url, refreshed_at, refresh_error, feed_token, sync_token`

func scanCalendar(row interface{ Scan(...any) error }) (*Calendar, error) {
	var c Calendar
	var source, refreshed, refreshErr, feed sql.NullString
	if err := row.Scan(&c.ID, &c.Name, &c.Color, &c.Position, &c.Hidden, &source, &refreshed, &refreshErr, &feed, &c.SyncToken); err != nil {
		return nil, err
	}
	c.FeedToken = nullString(feed)
	c.SourceURL = nullString(source)
	c.RefreshedAt = nullString(refreshed)
	c.RefreshError = nullString(refreshErr)
	return &c, nil
}

// ListCalendars returns every calendar, in display order.
func (s *Store) ListCalendars() ([]Calendar, error) {
	rows, err := s.db.Query(`SELECT ` + calendarColumns + ` FROM calendars ORDER BY position, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Calendar{}
	for rows.Next() {
		c, err := scanCalendar(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *c)
	}
	return list, rows.Err()
}

// GetCalendar returns one calendar.
func (s *Store) GetCalendar(id string) (*Calendar, error) {
	c, err := scanCalendar(s.db.QueryRow(`SELECT `+calendarColumns+` FROM calendars WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// CalendarByFeedToken returns the calendar published at a secret address.
func (s *Store) CalendarByFeedToken(token string) (*Calendar, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	c, err := scanCalendar(s.db.QueryRow(`SELECT `+calendarColumns+` FROM calendars WHERE feed_token = ?`, token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// SetFeedToken publishes a calendar at a new secret address (replacing the
// previous one), or unpublishes it when publish is false.
func (s *Store) SetFeedToken(id string, publish bool) (*Calendar, error) {
	var token any
	if publish {
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		token = base64.RawURLEncoding.EncodeToString(b)
	}
	res, err := s.db.Exec(`UPDATE calendars SET feed_token = ? WHERE id = ?`, token, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.GetCalendar(id)
}

// CalendarInput holds the fields of a new calendar. An empty ID gets a random
// one; an empty Color gets the next unused color.
type CalendarInput struct {
	ID        string
	Name      string
	Color     string
	SourceURL string
}

// CreateCalendar adds a calendar at the end of the list.
func (s *Store) CreateCalendar(in CalendarInput) (*Calendar, error) {
	name, err := cleanName(in.Name)
	if err != nil {
		return nil, err
	}
	id := in.ID
	if id == "" {
		id = newID()
	} else if !validResourceName(id) {
		return nil, invalid("Invalid calendar id")
	}
	color := in.Color
	if color != "" && !validColor(color) {
		return nil, invalid("Unknown color %q", color)
	}
	err = s.tx(func(tx *sql.Tx) error {
		if color == "" {
			color = nextColor(tx)
		}
		var position int
		if err := tx.QueryRow(`SELECT COALESCE(MAX(position) + 1, 0) FROM calendars`).Scan(&position); err != nil {
			return err
		}
		if taken, err := exists(tx, `SELECT 1 FROM calendars WHERE id = ?`, id); err != nil {
			return err
		} else if taken {
			return invalid("A calendar with this id already exists")
		}
		_, err := tx.Exec(`INSERT INTO calendars (id, name, color, position, source_url) VALUES (?, ?, ?, ?, ?)`,
			id, name, color, position, nullable(in.SourceURL))
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetCalendar(id)
}

// nextColor picks the first palette color not used yet, or cycles.
func nextColor(q querier) string {
	used := map[string]int{}
	rows, err := q.Query(`SELECT color FROM calendars`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var c string
			if rows.Scan(&c) == nil {
				used[c]++
			}
		}
	}
	best := Colors[0]
	for _, c := range Colors {
		if used[c] < used[best] {
			best = c
		}
	}
	return best
}

// CalendarUpdate lists the fields to change; nil fields are left as they are.
type CalendarUpdate struct {
	Name     *string
	Color    *string
	Hidden   *bool
	Position *int
}

// UpdateCalendar changes a calendar's name, color, visibility or position.
func (s *Store) UpdateCalendar(id string, up CalendarUpdate) (*Calendar, error) {
	if _, err := s.GetCalendar(id); err != nil {
		return nil, err
	}
	err := s.tx(func(tx *sql.Tx) error {
		if up.Name != nil {
			name, err := cleanName(*up.Name)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE calendars SET name = ? WHERE id = ?`, name, id); err != nil {
				return err
			}
		}
		if up.Color != nil {
			if !validColor(*up.Color) {
				return invalid("Unknown color %q", *up.Color)
			}
			if _, err := tx.Exec(`UPDATE calendars SET color = ? WHERE id = ?`, *up.Color, id); err != nil {
				return err
			}
		}
		if up.Hidden != nil {
			if _, err := tx.Exec(`UPDATE calendars SET hidden = ? WHERE id = ?`, *up.Hidden, id); err != nil {
				return err
			}
		}
		if up.Position != nil {
			if _, err := tx.Exec(`UPDATE calendars SET position = ? WHERE id = ?`, *up.Position, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetCalendar(id)
}

// DeleteCalendar removes a calendar and its events.
func (s *Store) DeleteCalendar(id string) error {
	res, err := s.db.Exec(`DELETE FROM calendars WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetRefreshResult records the outcome of a subscription refresh.
func (s *Store) SetRefreshResult(id, refreshErr string) error {
	_, err := s.db.Exec(`UPDATE calendars SET refreshed_at = ?, refresh_error = ? WHERE id = ?`, now(), nullable(refreshErr), id)
	return err
}

// validResourceName accepts the names used in CalDAV URLs: no slashes, no
// dot segments, printable ASCII.
func validResourceName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 {
		return false
	}
	for _, r := range name {
		if r <= ' ' || r > '~' || r == '/' || r == '\\' || r == '?' || r == '#' || r == '%' {
			return false
		}
	}
	return true
}
