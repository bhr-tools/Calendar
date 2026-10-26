package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"strings"
)

// MaxEnd is the LastEnd of endless recurring events.
const MaxEnd = math.MaxInt64

// Object is one iCalendar object of a calendar: an event with its overridden
// occurrences. Data is kept exactly as written; the other fields are derived
// from it by the caller so that queries do not need to parse iCalendar.
type Object struct {
	CalendarID string
	Name       string
	UID        string
	ETag       string
	Data       string
	// Search is the lowercase text matched by searches.
	Search string
	// FirstStart and LastEnd bound every occurrence, in Unix seconds.
	FirstStart int64
	LastEnd    int64
	ModifiedAt string
}

// UIDConflictError is returned when another object of the calendar already
// has the UID being written.
type UIDConflictError struct{ Name string }

func (e *UIDConflictError) Error() string { return "UID already used by " + e.Name }

// Condition is an HTTP precondition on a write: IfMatch is the expected ETag
// ("*" for any existing object), IfNoneMatch requires that no object exists.
type Condition struct {
	IfMatch     string
	IfNoneMatch bool
}

func etagOf(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:12])
}

const objectColumns = `calendar_id, name, uid, etag, data, search, first_start, last_end, modified_at`

func scanObject(row interface{ Scan(...any) error }) (*Object, error) {
	var o Object
	err := row.Scan(&o.CalendarID, &o.Name, &o.UID, &o.ETag, &o.Data, &o.Search, &o.FirstStart, &o.LastEnd, &o.ModifiedAt)
	return &o, err
}

func collectObjects(rows *sql.Rows, err error) ([]Object, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Object{}
	for rows.Next() {
		o, err := scanObject(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *o)
	}
	return list, rows.Err()
}

// GetObject returns one object of a calendar.
func (s *Store) GetObject(calendarID, name string) (*Object, error) {
	o, err := scanObject(s.db.QueryRow(`SELECT `+objectColumns+` FROM objects WHERE calendar_id = ? AND name = ?`, calendarID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return o, err
}

// ListObjects returns every object of a calendar.
func (s *Store) ListObjects(calendarID string) ([]Object, error) {
	return collectObjects(s.db.Query(`SELECT `+objectColumns+` FROM objects WHERE calendar_id = ? ORDER BY name`, calendarID))
}

// ObjectsInRange returns the objects of a calendar with an occurrence that may
// overlap [from, to) (Unix seconds).
func (s *Store) ObjectsInRange(calendarID string, from, to int64) ([]Object, error) {
	return collectObjects(s.db.Query(`SELECT `+objectColumns+` FROM objects
		WHERE calendar_id = ? AND first_start < ? AND last_end > ? ORDER BY first_start`, calendarID, to, from))
}

// SearchObjects returns up to limit objects whose text contains every word of
// query, across all calendars.
func (s *Store) SearchObjects(query string, limit int) ([]Object, error) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return []Object{}, nil
	}
	where := make([]string, len(words))
	args := make([]any, 0, len(words)+1)
	for i, w := range words {
		where[i] = `instr(search, ?) > 0`
		args = append(args, w)
	}
	args = append(args, limit)
	return collectObjects(s.db.Query(`SELECT `+objectColumns+` FROM objects WHERE `+strings.Join(where, " AND ")+
		` ORDER BY first_start DESC LIMIT ?`, args...))
}

// bump records a change of an object in the calendar's sync log.
func bump(tx *sql.Tx, calendarID, name string, deleted bool) error {
	var token int64
	if err := tx.QueryRow(`UPDATE calendars SET sync_token = sync_token + 1 WHERE id = ? RETURNING sync_token`, calendarID).Scan(&token); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	_, err := tx.Exec(`INSERT INTO changes (calendar_id, name, token, deleted) VALUES (?, ?, ?, ?)
		ON CONFLICT(calendar_id, name) DO UPDATE SET token = excluded.token, deleted = excluded.deleted`,
		calendarID, name, token, deleted)
	return err
}

func checkWritable(q querier, calendarID string) error {
	var source sql.NullString
	err := q.QueryRow(`SELECT source_url FROM calendars WHERE id = ?`, calendarID).Scan(&source)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if source.Valid {
		return ErrReadOnly
	}
	return nil
}

func checkCondition(q querier, calendarID, name string, cond Condition) (existing *Object, err error) {
	existing, err = scanObject(q.QueryRow(`SELECT `+objectColumns+` FROM objects WHERE calendar_id = ? AND name = ?`, calendarID, name))
	if errors.Is(err, sql.ErrNoRows) {
		existing, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	switch {
	case cond.IfNoneMatch && existing != nil:
		return nil, ErrPrecondition
	case cond.IfMatch == "*" && existing == nil:
		return nil, ErrPrecondition
	case cond.IfMatch != "" && cond.IfMatch != "*" && (existing == nil || existing.ETag != cond.IfMatch):
		return nil, ErrPrecondition
	}
	return existing, nil
}

// PutObject creates or replaces an object and returns it with its new ETag.
func (s *Store) PutObject(o Object, cond Condition) (*Object, bool, error) {
	if !validResourceName(o.Name) {
		return nil, false, invalid("Invalid resource name")
	}
	o.ETag = etagOf(o.Data)
	o.ModifiedAt = now()
	created := false
	err := s.tx(func(tx *sql.Tx) error {
		if err := checkWritable(tx, o.CalendarID); err != nil {
			return err
		}
		existing, err := checkCondition(tx, o.CalendarID, o.Name, cond)
		if err != nil {
			return err
		}
		created = existing == nil
		return putObject(tx, o)
	})
	if err != nil {
		return nil, false, err
	}
	return &o, created, nil
}

func putObject(tx *sql.Tx, o Object) error {
	var other string
	err := tx.QueryRow(`SELECT name FROM objects WHERE calendar_id = ? AND uid = ? AND name != ?`, o.CalendarID, o.UID, o.Name).Scan(&other)
	if err == nil {
		return &UIDConflictError{Name: other}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO objects (`+objectColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(calendar_id, name) DO UPDATE SET uid = excluded.uid, etag = excluded.etag, data = excluded.data,
			search = excluded.search, first_start = excluded.first_start, last_end = excluded.last_end, modified_at = excluded.modified_at`,
		o.CalendarID, o.Name, o.UID, o.ETag, o.Data, o.Search, o.FirstStart, o.LastEnd, o.ModifiedAt); err != nil {
		return err
	}
	return bump(tx, o.CalendarID, o.Name, false)
}

// DeleteObject removes an object.
func (s *Store) DeleteObject(calendarID, name string, cond Condition) error {
	return s.tx(func(tx *sql.Tx) error {
		if err := checkWritable(tx, calendarID); err != nil {
			return err
		}
		existing, err := checkCondition(tx, calendarID, name, cond)
		if err != nil {
			return err
		}
		if existing == nil {
			return ErrNotFound
		}
		return deleteObject(tx, calendarID, name)
	})
}

func deleteObject(tx *sql.Tx, calendarID, name string) error {
	if _, err := tx.Exec(`DELETE FROM objects WHERE calendar_id = ? AND name = ?`, calendarID, name); err != nil {
		return err
	}
	return bump(tx, calendarID, name, true)
}

// MoveObject moves an object to another calendar, keeping its name unless
// newName is set.
func (s *Store) MoveObject(fromID, name, toID, newName string, cond Condition) (*Object, error) {
	if newName == "" {
		newName = name
	}
	if !validResourceName(newName) {
		return nil, invalid("Invalid resource name")
	}
	var moved Object
	err := s.tx(func(tx *sql.Tx) error {
		if err := checkWritable(tx, fromID); err != nil {
			return err
		}
		if err := checkWritable(tx, toID); err != nil {
			return err
		}
		existing, err := checkCondition(tx, fromID, name, cond)
		if err != nil {
			return err
		}
		if existing == nil {
			return ErrNotFound
		}
		if fromID == toID && name == newName {
			moved = *existing
			return nil
		}
		if taken, err := exists(tx, `SELECT 1 FROM objects WHERE calendar_id = ? AND name = ?`, toID, newName); err != nil {
			return err
		} else if taken {
			return ErrPrecondition
		}
		if err := deleteObject(tx, fromID, name); err != nil {
			return err
		}
		moved = *existing
		moved.CalendarID, moved.Name, moved.ModifiedAt = toID, newName, now()
		return putObject(tx, moved)
	})
	if err != nil {
		return nil, err
	}
	return &moved, nil
}

// Change is an entry of a calendar's sync log.
type Change struct {
	Name    string
	Deleted bool
}

// Changes returns the objects changed after token, and the calendar's current
// token. Token 0 lists every existing object.
func (s *Store) Changes(calendarID string, token int64) ([]Change, int64, error) {
	var current int64
	var list []Change
	err := s.tx(func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT sync_token FROM calendars WHERE id = ?`, calendarID).Scan(&current); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if token > current {
			return ErrPrecondition
		}
		var rows *sql.Rows
		var err error
		if token == 0 {
			rows, err = tx.Query(`SELECT name, 0 FROM objects WHERE calendar_id = ? ORDER BY name`, calendarID)
		} else {
			rows, err = tx.Query(`SELECT name, deleted FROM changes WHERE calendar_id = ? AND token > ? ORDER BY token`, calendarID, token)
		}
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Change
			if err := rows.Scan(&c.Name, &c.Deleted); err != nil {
				return err
			}
			list = append(list, c)
		}
		return rows.Err()
	})
	return list, current, err
}

// ReplaceObjects makes a subscription hold exactly objs: new and changed
// objects are written, missing ones deleted, identical ones left alone (so
// clients do not download them again).
func (s *Store) ReplaceObjects(calendarID string, objs []Object) error {
	return s.tx(func(tx *sql.Tx) error {
		current := map[string]string{}
		rows, err := tx.Query(`SELECT name, etag FROM objects WHERE calendar_id = ?`, calendarID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var name, etag string
			if err := rows.Scan(&name, &etag); err != nil {
				rows.Close()
				return err
			}
			current[name] = etag
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		keep := map[string]bool{}
		for _, o := range objs {
			o.CalendarID = calendarID
			o.ETag = etagOf(o.Data)
			o.ModifiedAt = now()
			keep[o.Name] = true
			if current[o.Name] == o.ETag {
				continue
			}
			// A feed may move a UID to another name: free it first.
			var other string
			err := tx.QueryRow(`SELECT name FROM objects WHERE calendar_id = ? AND uid = ? AND name != ?`, calendarID, o.UID, o.Name).Scan(&other)
			if err == nil {
				if err := deleteObject(tx, calendarID, other); err != nil {
					return err
				}
				delete(current, other)
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err := putObject(tx, o); err != nil {
				return err
			}
		}
		for name := range current {
			if !keep[name] {
				if err := deleteObject(tx, calendarID, name); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
