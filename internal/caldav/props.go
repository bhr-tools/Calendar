package caldav

import (
	"encoding/xml"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bhr-tools/calendar/internal/db"
)

// target is a resource with the data needed to render its properties.
type target struct {
	res      resource
	calendar *db.Calendar
	object   *db.Object
}

const (
	privilegesWrite = "<d:privilege><d:all/></d:privilege><d:privilege><d:read/></d:privilege>" +
		"<d:privilege><d:write/></d:privilege><d:privilege><d:write-properties/></d:privilege>" +
		"<d:privilege><d:write-content/></d:privilege><d:privilege><d:bind/></d:privilege>" +
		"<d:privilege><d:unbind/></d:privilege><d:privilege><d:read-current-user-privilege-set/></d:privilege>" +
		"<d:privilege><c:read-free-busy/></d:privilege>"
	privilegesRead = "<d:privilege><d:read/></d:privilege>" +
		"<d:privilege><d:read-current-user-privilege-set/></d:privilege>" +
		"<d:privilege><c:read-free-busy/></d:privilege>"
	principalHref = Prefix + "/principal/"
	homeHref      = Prefix + "/calendars/"
)

func href(p string) string {
	return "<d:href>" + escape(p) + "</d:href>"
}

func reportSet(names ...string) string {
	var b strings.Builder
	for _, n := range names {
		b.WriteString("<d:supported-report><d:report>" + n + "</d:report></d:supported-report>")
	}
	return b.String()
}

// prop renders one property of t. calendar-data is only rendered when asked
// for explicitly (never for allprop).
func (t target) prop(n xml.Name) (string, bool) {
	switch n.Space {
	case nsDAV:
		switch n.Local {
		case "current-user-principal":
			return href(principalHref), true
		case "principal-collection-set":
			return href(principalHref), true
		case "owner":
			return href(principalHref), true
		case "resourcetype":
			switch t.res.kind {
			case kindRoot, kindHome:
				return "<d:collection/>", true
			case kindPrincipal:
				return "<d:principal/>", true
			case kindCalendar:
				return "<d:collection/><c:calendar/>", true
			case kindObject:
				return "", true
			}
		case "displayname":
			switch t.res.kind {
			case kindCalendar:
				return escape(t.calendar.Name), true
			case kindPrincipal:
				return "Calendar", true
			}
		case "principal-URL":
			if t.res.kind == kindPrincipal {
				return href(principalHref), true
			}
		case "current-user-privilege-set":
			if t.res.kind == kindCalendar && t.calendar.ReadOnly() {
				return privilegesRead, true
			}
			if t.res.kind == kindObject || t.res.kind == kindCalendar || t.res.kind == kindHome {
				return privilegesWrite, true
			}
			return privilegesRead, true
		case "supported-report-set":
			if t.res.kind == kindCalendar {
				return reportSet("<c:calendar-multiget/>", "<c:calendar-query/>", "<d:sync-collection/>"), true
			}
			return "", true
		case "sync-token":
			if t.res.kind == kindCalendar {
				return escape(syncToken(t.calendar.SyncToken)), true
			}
		case "getetag":
			switch t.res.kind {
			case kindObject:
				return escape(quote(t.object.ETag)), true
			case kindCalendar:
				return escape(quote("ctag-" + strconv.FormatInt(t.calendar.SyncToken, 10))), true
			}
		case "getcontenttype":
			switch t.res.kind {
			case kindObject:
				return "text/calendar; charset=utf-8; component=vevent", true
			case kindCalendar:
				return "text/calendar; charset=utf-8", true
			}
		case "getcontentlength":
			if t.res.kind == kindObject {
				return strconv.Itoa(len(t.object.Data)), true
			}
		case "getlastmodified":
			if t.res.kind == kindObject {
				if m, err := time.Parse(time.RFC3339, t.object.ModifiedAt); err == nil {
					return m.UTC().Format(http.TimeFormat), true
				}
			}
		}
	case nsCalDAV:
		switch n.Local {
		case "calendar-home-set":
			if t.res.kind == kindPrincipal || t.res.kind == kindRoot {
				return href(homeHref), true
			}
		case "calendar-user-address-set":
			if t.res.kind == kindPrincipal {
				return href(principalHref), true
			}
		case "supported-calendar-component-set":
			if t.res.kind == kindCalendar {
				return `<c:comp name="VEVENT"/>`, true
			}
		case "supported-calendar-data":
			if t.res.kind == kindCalendar {
				return `<c:calendar-data content-type="text/calendar" version="2.0"/>`, true
			}
		case "max-resource-size":
			if t.res.kind == kindCalendar {
				return strconv.Itoa(maxObjectSize), true
			}
		case "calendar-data":
			if t.res.kind == kindObject {
				return escape(t.object.Data), true
			}
		case "calendar-description":
			if t.res.kind == kindCalendar {
				return "", true
			}
		}
	case nsCS:
		switch n.Local {
		case "getctag":
			if t.res.kind == kindCalendar {
				return strconv.FormatInt(t.calendar.SyncToken, 10), true
			}
		}
	case nsApple:
		switch n.Local {
		case "calendar-color":
			if t.res.kind == kindCalendar {
				return Colors[t.calendar.Color], true
			}
		case "calendar-order":
			if t.res.kind == kindCalendar {
				return strconv.Itoa(t.calendar.Position + 1), true
			}
		}
	}
	return "", false
}

// allProps is what an allprop PROPFIND returns.
var allProps = []xml.Name{
	name(nsDAV, "resourcetype"), name(nsDAV, "displayname"), name(nsDAV, "getetag"),
	name(nsDAV, "getcontenttype"), name(nsDAV, "getcontentlength"), name(nsDAV, "getlastmodified"),
}

func (t target) render(names []xml.Name, allprop bool) []propResult {
	var out []propResult
	for _, n := range names {
		v, ok := t.prop(n)
		if allprop && !ok {
			continue
		}
		out = append(out, propResult{name: n, value: v, found: ok})
	}
	return out
}

// ---------------------------------------------------------------------------
// PROPFIND
// ---------------------------------------------------------------------------

func (h *Handler) target(res resource) (target, error) {
	t := target{res: res}
	switch res.kind {
	case kindUnknown:
		return t, db.ErrNotFound
	case kindCalendar:
		c, err := h.Store.GetCalendar(res.calendar)
		if err != nil {
			return t, err
		}
		t.calendar = c
	case kindObject:
		c, err := h.Store.GetCalendar(res.calendar)
		if err != nil {
			return t, err
		}
		o, err := h.Store.GetObject(res.calendar, res.object)
		if err != nil {
			return t, err
		}
		t.calendar, t.object = c, o
	}
	return t, nil
}

func (h *Handler) propfind(w http.ResponseWriter, r *http.Request, res resource) error {
	root, err := readBody(r)
	if err != nil {
		return err
	}
	names, allprop := allProps, true
	if prop := root.child(nsDAV, "prop"); prop != nil {
		names, allprop = prop.propNames(), false
	}
	self, err := h.target(res)
	if err != nil {
		return err
	}
	m := newMultistatus()
	m.response(res.href(), self.render(names, allprop))

	// Depth 0 lists the resource itself; anything else also lists its
	// children (infinity is treated as 1).
	if strings.TrimSpace(r.Header.Get("Depth")) != "0" {
		switch res.kind {
		case kindRoot:
			for _, k := range []kind{kindPrincipal, kindHome} {
				child := target{res: resource{kind: k}}
				m.response(child.res.href(), child.render(names, allprop))
			}
		case kindHome:
			calendars, err := h.Store.ListCalendars()
			if err != nil {
				return err
			}
			for i := range calendars {
				child := target{res: resource{kind: kindCalendar, calendar: calendars[i].ID}, calendar: &calendars[i]}
				m.response(child.res.href(), child.render(names, allprop))
			}
		case kindCalendar:
			objs, err := h.Store.ListObjects(res.calendar)
			if err != nil {
				return err
			}
			for i := range objs {
				child := target{res: resource{kind: kindObject, calendar: res.calendar, object: objs[i].Name}, calendar: self.calendar, object: &objs[i]}
				m.response(child.res.href(), child.render(names, allprop))
			}
		}
	}
	writeMultistatus(w, m)
	return nil
}

// ---------------------------------------------------------------------------
// REPORT
// ---------------------------------------------------------------------------

func (h *Handler) report(w http.ResponseWriter, r *http.Request, res resource) error {
	root, err := readBody(r)
	if err != nil {
		return err
	}
	switch {
	case root.is(nsCalDAV, "calendar-multiget"):
		return h.multiget(w, root)
	case root.is(nsCalDAV, "calendar-query"):
		return h.query(w, root, res)
	case root.is(nsDAV, "sync-collection"):
		return h.syncCollection(w, root, res)
	}
	davError(w, http.StatusForbidden, "<d:supported-report/>")
	return nil
}

func (h *Handler) multiget(w http.ResponseWriter, root *node) error {
	names := root.child(nsDAV, "prop").propNames()
	m := newMultistatus()
	calendars := map[string]*db.Calendar{}
	for _, n := range root.children {
		if !n.is(nsDAV, "href") {
			continue
		}
		res := hrefPath(n.text)
		if res.kind != kindObject {
			m.status(strings.TrimSpace(n.text), http.StatusNotFound, "Not Found")
			continue
		}
		c, ok := calendars[res.calendar]
		if !ok {
			c, _ = h.Store.GetCalendar(res.calendar)
			calendars[res.calendar] = c
		}
		var o *db.Object
		if c != nil {
			o, _ = h.Store.GetObject(res.calendar, res.object)
		}
		if o == nil {
			m.status(res.href(), http.StatusNotFound, "Not Found")
			continue
		}
		t := target{res: res, calendar: c, object: o}
		m.response(res.href(), t.render(names, false))
	}
	writeMultistatus(w, m)
	return nil
}

// query answers calendar-query: every event of the calendar, or those that
// may overlap the requested time-range. Other components (tasks) never match.
func (h *Handler) query(w http.ResponseWriter, root *node, res resource) error {
	if res.kind != kindCalendar {
		return &httpError{http.StatusBadRequest, "calendar-query is only allowed on a calendar"}
	}
	t, err := h.target(res)
	if err != nil {
		return err
	}
	names := root.child(nsDAV, "prop").propNames()
	m := newMultistatus()

	filter := root.child(nsCalDAV, "filter")
	vcal := filter.child(nsCalDAV, "comp-filter")
	comp := vcal.child(nsCalDAV, "comp-filter")
	if comp != nil && !strings.EqualFold(comp.attr("name"), "VEVENT") {
		writeMultistatus(w, m)
		return nil
	}
	from, to := int64(-1<<62), int64(1<<62)
	if tr := comp.child(nsCalDAV, "time-range"); tr != nil {
		if v, err := time.Parse("20060102T150405Z", tr.attr("start")); err == nil {
			from = v.Unix()
		}
		if v, err := time.Parse("20060102T150405Z", tr.attr("end")); err == nil {
			to = v.Unix()
		}
	}
	objs, err := h.Store.ObjectsInRange(res.calendar, from, to)
	if err != nil {
		return err
	}
	for i := range objs {
		child := target{res: resource{kind: kindObject, calendar: res.calendar, object: objs[i].Name}, calendar: t.calendar, object: &objs[i]}
		m.response(child.res.href(), child.render(names, false))
	}
	writeMultistatus(w, m)
	return nil
}

// syncCollection answers sync-collection (RFC 6578) from the change log.
func (h *Handler) syncCollection(w http.ResponseWriter, root *node, res resource) error {
	if res.kind != kindCalendar {
		return &httpError{http.StatusForbidden, "sync-collection is only allowed on a calendar"}
	}
	t, err := h.target(res)
	if err != nil {
		return err
	}
	token, ok := parseSyncToken(root.child(nsDAV, "sync-token").textOrEmpty())
	if !ok {
		davError(w, http.StatusForbidden, "<d:valid-sync-token/>")
		return nil
	}
	changes, current, err := h.Store.Changes(res.calendar, token)
	if err == db.ErrPrecondition {
		davError(w, http.StatusForbidden, "<d:valid-sync-token/>")
		return nil
	}
	if err != nil {
		return err
	}
	names := root.child(nsDAV, "prop").propNames()
	m := newMultistatus()
	for _, c := range changes {
		res := resource{kind: kindObject, calendar: res.calendar, object: c.Name}
		if c.Deleted {
			m.status(res.href(), http.StatusNotFound, "Not Found")
			continue
		}
		o, err := h.Store.GetObject(res.calendar, c.Name)
		if err != nil {
			m.status(res.href(), http.StatusNotFound, "Not Found")
			continue
		}
		child := target{res: res, calendar: t.calendar, object: o}
		m.response(res.href(), child.render(names, false))
	}
	m.raw("<d:sync-token>" + escape(syncToken(current)) + "</d:sync-token>")
	writeMultistatus(w, m)
	return nil
}

func (n *node) textOrEmpty() string {
	if n == nil {
		return ""
	}
	return n.text
}
