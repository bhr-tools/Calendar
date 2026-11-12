package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// An app connected to one calendar reads its events, and nothing else.
func TestConnectedApp(t *testing.T) {
	srv, calID := setup(t)
	other := map[string]any{}
	call(t, srv, "POST", "/api/calendars", map[string]any{"name": "Private"}, &other)
	call(t, srv, "POST", "/api/events", map[string]any{"calendar_id": calID, "title": "Examen Automates", "start": "2027-01-12", "end": "2027-01-13", "all_day": true}, nil)
	call(t, srv, "POST", "/api/events", map[string]any{"calendar_id": other["id"], "title": "Secret", "start": "2027-01-12", "end": "2027-01-13", "all_day": true}, nil)

	back := "https://assistant.example.com/api/integrations/callback"
	if s := call(t, srv, "POST", "/api/connect", map[string]any{"app": "Planner", "redirect_uri": "javascript:x", "calendar_id": calID}, nil); s != 400 {
		t.Fatalf("bad redirect: %d", s)
	}
	var code struct{ Code string }
	call(t, srv, "POST", "/api/connect", map[string]any{"app": "Planner", "redirect_uri": back, "calendar_id": calID}, &code)
	var tok struct{ Token, ID, Name string }
	if s := call(t, srv, "POST", "/api/connect/token", map[string]any{"code": code.Code, "redirect_uri": back}, &tok); s != 200 || tok.Token == "" || tok.Name != "Personal" {
		t.Fatalf("token: %d %+v", s, tok)
	}
	if s := call(t, srv, "POST", "/api/connect/token", map[string]any{"code": code.Code, "redirect_uri": back}, nil); s != 400 {
		t.Fatalf("a code is used once: %d", s)
	}
	app := func(path string, want int) string {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+tok.Token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s = %d, want %d: %s", path, res.StatusCode, want, b)
		}
		return string(b)
	}
	if got := app("/api/app/info", 200); !strings.Contains(got, `"scope":"calendar"`) || !strings.Contains(got, `"name":"Personal"`) {
		t.Fatalf("info: %s", got)
	}
	got := app("/api/app/events?from=2027-01-01&to=2027-02-01", 200)
	if !strings.Contains(got, `"title":"Examen Automates"`) || !strings.Contains(got, `"start":"2027-01-12"`) || strings.Contains(got, "Secret") {
		t.Fatalf("events: %s", got)
	}
	var apps []map[string]any
	call(t, srv, "GET", "/api/apps", nil, &apps)
	if len(apps) != 1 || apps[0]["calendar_name"] != "Personal" || apps[0]["used_at"] == nil {
		t.Fatalf("apps: %v", apps)
	}
	call(t, srv, "DELETE", "/api/calendars/"+calID, nil, nil)
	app("/api/app/info", http.StatusUnauthorized)
}
