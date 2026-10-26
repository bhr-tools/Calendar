package caldav

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// XML namespaces of WebDAV, CalDAV and the extensions Apple clients use.
const (
	nsDAV    = "DAV:"
	nsCalDAV = "urn:ietf:params:xml:ns:caldav"
	nsCS     = "http://calendarserver.org/ns/"
	nsApple  = "http://apple.com/ns/ical/"
)

var prefixes = map[string]string{nsDAV: "d", nsCalDAV: "c", nsCS: "cs", nsApple: "ical"}

// node is a parsed XML element.
type node struct {
	name     xml.Name
	attrs    []xml.Attr
	children []*node
	text     string
}

// parseXML reads a request body into a tree. An empty body gives nil.
func parseXML(r io.Reader) (*node, error) {
	dec := xml.NewDecoder(r)
	var stack []*node
	var root *node
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{name: t.Name, attrs: t.Attr}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, n)
			} else if root == nil {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text += string(t)
			}
		}
	}
	return root, nil
}

func (n *node) is(space, local string) bool {
	return n != nil && n.name.Space == space && n.name.Local == local
}

// child returns the first child element with that name.
func (n *node) child(space, local string) *node {
	if n == nil {
		return nil
	}
	for _, c := range n.children {
		if c.is(space, local) {
			return c
		}
	}
	return nil
}

// find returns the first descendant with that name, depth first.
func (n *node) find(space, local string) *node {
	if n == nil {
		return nil
	}
	for _, c := range n.children {
		if c.is(space, local) {
			return c
		}
		if found := c.find(space, local); found != nil {
			return found
		}
	}
	return nil
}

func (n *node) attr(local string) string {
	for _, a := range n.attrs {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// propNames lists the properties requested by a <prop> element.
func (n *node) propNames() []xml.Name {
	if n == nil {
		return nil
	}
	names := make([]xml.Name, 0, len(n.children))
	for _, c := range n.children {
		names = append(names, c.name)
	}
	return names
}

// textEscaper escapes character data. Quotes stay as they are: ETags are
// quoted strings, and some clients compare them without decoding entities.
var textEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\r", "&#13;")

func escape(s string) string {
	return textEscaper.Replace(s)
}

// multistatus builds a 207 Multi-Status body.
type multistatus struct {
	b      strings.Builder
	extra  map[string]string // namespace -> prefix for unknown namespaces
	closed bool
}

func newMultistatus() *multistatus {
	m := &multistatus{extra: map[string]string{}}
	m.b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	m.b.WriteString(`<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:cs="http://calendarserver.org/ns/" xmlns:ical="http://apple.com/ns/ical/">`)
	return m
}

// open and close tags of an element, declaring its namespace when it is not
// one of the usual ones.
func tag(name xml.Name) (open, closeTag string) {
	if p, ok := prefixes[name.Space]; ok {
		return "<" + p + ":" + name.Local, "</" + p + ":" + name.Local + ">"
	}
	if name.Space == "" {
		return "<" + name.Local, "</" + name.Local + ">"
	}
	return fmt.Sprintf(`<x:%s xmlns:x="%s"`, name.Local, strings.ReplaceAll(escape(name.Space), `"`, "&quot;")), "</x:" + name.Local + ">"
}

// element renders <name>inner</name>; inner is raw XML.
func element(name xml.Name, inner string) string {
	open, closeTag := tag(name)
	if inner == "" {
		return open + "/>"
	}
	return open + ">" + inner + closeTag
}

// propResult is the value of one property: found (with its XML content) or
// not.
type propResult struct {
	name  xml.Name
	value string
	found bool
}

// response adds a <response> for href with its found and missing properties.
func (m *multistatus) response(href string, props []propResult) {
	m.b.WriteString("<d:response><d:href>" + escape(href) + "</d:href>")
	var found, missing strings.Builder
	for _, p := range props {
		if p.found {
			found.WriteString(element(p.name, p.value))
		} else {
			missing.WriteString(element(p.name, ""))
		}
	}
	if found.Len() > 0 || missing.Len() == 0 {
		m.b.WriteString("<d:propstat><d:prop>" + found.String() + "</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>")
	}
	if missing.Len() > 0 {
		m.b.WriteString("<d:propstat><d:prop>" + missing.String() + "</d:prop><d:status>HTTP/1.1 404 Not Found</d:status></d:propstat>")
	}
	m.b.WriteString("</d:response>")
}

// status adds a <response> carrying only a status (deleted or missing
// resources).
func (m *multistatus) status(href string, code int, text string) {
	m.b.WriteString(fmt.Sprintf("<d:response><d:href>%s</d:href><d:status>HTTP/1.1 %d %s</d:status></d:response>", escape(href), code, text))
}

// raw appends XML inside the multistatus (sync-token).
func (m *multistatus) raw(s string) {
	m.b.WriteString(s)
}

func (m *multistatus) String() string {
	if !m.closed {
		m.b.WriteString("</d:multistatus>")
		m.closed = true
	}
	return m.b.String()
}

func name(space, local string) xml.Name {
	return xml.Name{Space: space, Local: local}
}
