// Package feeds keeps subscribed calendars in sync with their iCalendar feed.
package feeds

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-ical"

	"github.com/bhr-tools/calendar/internal/cal"
	"github.com/bhr-tools/calendar/internal/db"
)

const maxFeedSize = 20 << 20 // 20 MiB

// Refresher downloads subscriptions periodically.
type Refresher struct {
	Store    *db.Store
	Loc      *time.Location
	Interval time.Duration
	Client   *http.Client

	mu sync.Mutex // one refresh at a time
}

// Run refreshes every subscription now and then every Interval, until ctx is
// done.
func (r *Refresher) Run(ctx context.Context) {
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	for {
		r.RefreshAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RefreshAll refreshes every subscription, logging failures.
func (r *Refresher) RefreshAll(ctx context.Context) {
	calendars, err := r.Store.ListCalendars()
	if err != nil {
		log.Printf("feeds: %v", err)
		return
	}
	for _, c := range calendars {
		if c.ReadOnly() {
			if err := r.Refresh(ctx, c.ID); err != nil {
				log.Printf("feeds: %s: %v", c.Name, err)
			}
		}
	}
}

// Refresh downloads one subscription and stores its events. The outcome is
// recorded on the calendar.
func (r *Refresher) Refresh(ctx context.Context, calendarID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, err := r.Store.GetCalendar(calendarID)
	if err != nil {
		return err
	}
	if !c.ReadOnly() {
		return errors.New("not a subscription")
	}
	objs, err := r.fetch(ctx, c.SourceURL)
	if err == nil {
		err = r.Store.ReplaceObjects(c.ID, objs)
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if serr := r.Store.SetRefreshResult(c.ID, msg); serr != nil && err == nil {
		err = serr
	}
	return err
}

func (r *Refresher) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (r *Refresher) fetch(ctx context.Context, url string) ([]db.Object, error) {
	// webcal:// is the usual scheme of subscription links.
	if rest, ok := strings.CutPrefix(url, "webcal://"); ok {
		url = "https://" + rest
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %w", err)
	}
	req.Header.Set("Accept", "text/calendar")
	res, err := r.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed: HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxFeedSize+1))
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	if len(body) > maxFeedSize {
		return nil, errors.New("the feed is too large")
	}
	return Split(string(body), r.Loc)
}

// exportStamp matches the export date ADE (university timetables) adds to
// every description; it changes on each download without any real change.
var exportStamp = regexp.MustCompile(`\s*\(Export[ée] le ?:[^)]*\)`)

var safeName = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,200}$`)

// Split turns an iCalendar feed into one object per event (UID), normalized
// so that an unchanged event gives the same data on every download.
func Split(data string, loc *time.Location) ([]db.Object, error) {
	return split(data, loc, true)
}

// SplitImport turns an iCalendar file into one object per event (UID), kept
// as it is. Objects are named after their UID when it makes a valid name.
func SplitImport(data string, loc *time.Location) ([]db.Object, error) {
	return split(data, loc, false)
}

func split(data string, loc *time.Location, feed bool) ([]db.Object, error) {
	parsed, err := cal.Parse(data)
	if err != nil {
		return nil, err
	}
	var zones []*ical.Component
	groups := map[string][]*ical.Component{}
	var order []string
	for _, child := range parsed.Children {
		switch child.Name {
		case ical.CompTimezone:
			zones = append(zones, child)
		case ical.CompEvent:
			uid := ""
			if p := child.Props.Get(ical.PropUID); p != nil {
				uid = p.Value
			}
			if uid == "" {
				continue
			}
			if feed {
				normalize(child)
			} else if child.Props.Get(ical.PropDateTimeStamp) == nil {
				child.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
			}
			if _, ok := groups[uid]; !ok {
				order = append(order, uid)
			}
			groups[uid] = append(groups[uid], child)
		}
	}

	objs := make([]db.Object, 0, len(order))
	for _, uid := range order {
		c := ical.NewCalendar()
		c.Props.SetText(ical.PropProductID, cal.ProdID)
		c.Props.SetText(ical.PropVersion, "2.0")
		c.Children = append(append(c.Children, zones...), groups[uid]...)
		info, err := cal.Inspect(c, loc)
		if err != nil {
			continue // skip broken events, keep the rest of the feed
		}
		text, err := cal.Encode(c)
		if err != nil {
			continue
		}
		sum := sha256.Sum256([]byte(uid))
		name := hex.EncodeToString(sum[:10]) + ".ics"
		if !feed && safeName.MatchString(uid) {
			name = uid + ".ics"
		}
		objs = append(objs, db.Object{
			Name:       name,
			UID:        uid,
			Data:       text,
			Search:     info.Search,
			FirstStart: info.First,
			LastEnd:    info.LastEnd,
		})
	}
	return objs, nil
}

// normalize removes what changes on every download: timestamps of the export
// itself, not of the event.
func normalize(e *ical.Component) {
	e.Props.Set(&ical.Prop{Name: ical.PropDateTimeStamp, Params: ical.Params{}, Value: "19700101T000000Z"})
	e.Props.Del(ical.PropLastModified)
	if p := e.Props.Get(ical.PropDescription); p != nil {
		if text, err := p.Text(); err == nil {
			text = strings.TrimSpace(exportStamp.ReplaceAllString(text, ""))
			if text == "" {
				e.Props.Del(ical.PropDescription)
			} else {
				e.Props.SetText(ical.PropDescription, text)
			}
		}
	}
}
