package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/clientcontext"
)

func TestDoBdListRemoteJSONRoutesToCity(t *testing.T) {
	t.Setenv("GC_HOME", t.TempDir())

	var gotPath string
	var gotQuery string
	srv := newEventsTestServer(t, testEventRoutes{cityBeads: func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":"remote-task-1","title":"remote task","status":"open","issue_type":"task","created_at":"2026-09-16T00:00:00Z","updated_at":"2026-09-16T00:00:00Z"}],"total":1}`))
	}})
	defer srv.Close()

	var seed, seedErr bytes.Buffer
	if code := doContextAdd(clientcontext.Context{
		Name: "prod",
		URL:  srv.URL,
		City: "mc-city",
	}, &seed, &seedErr); code != 0 {
		t.Fatalf("seed context: %q", seedErr.String())
	}
	setProdContextFlag(t)

	var out, errb bytes.Buffer
	code := doBd([]string{"list", "--json", "--status", "open"}, &out, &errb)
	if code != 0 {
		t.Fatalf("doBd remote list = %d, want 0; stdout=%q stderr=%q", code, out.String(), errb.String())
	}
	if gotPath != "/v0/city/mc-city/beads" {
		t.Fatalf("remote path = %q, want /v0/city/mc-city/beads", gotPath)
	}
	if !strings.Contains(gotQuery, "status=open") {
		t.Fatalf("remote query = %q, want status=open", gotQuery)
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("remote bd list JSON: %v; output=%q", err, out.String())
	}
	if len(rows) != 1 || rows[0].ID != "remote-task-1" {
		t.Fatalf("remote rows = %+v, want remote-task-1", rows)
	}
}

func TestDoBdShowRemoteStillUsesCapabilityGate(t *testing.T) {
	t.Setenv("GC_HOME", t.TempDir())

	srv := newEventsTestServer(t, testEventRoutes{})
	defer srv.Close()
	var seed, seedErr bytes.Buffer
	if code := doContextAdd(clientcontext.Context{
		Name: "prod",
		URL:  srv.URL,
		City: "mc-city",
	}, &seed, &seedErr); code != 0 {
		t.Fatalf("seed context: %q", seedErr.String())
	}
	setProdContextFlag(t)

	var out, errb bytes.Buffer
	code := doBd([]string{"show", "gcg-remote"}, &out, &errb)
	if code == 0 {
		t.Fatalf("doBd remote show = 0, want capability refusal; stdout=%q", out.String())
	}
	want := "gc bd: this command does not support a remote city (--city-url/--context) yet; remote support is being enabled incrementally\n"
	if got := errb.String(); got != want {
		t.Fatalf("remote show refusal = %q, want %q", got, want)
	}
}
