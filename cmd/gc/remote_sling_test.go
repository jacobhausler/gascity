package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// remoteSlingFake is a stand-in for a REMOTE city's control plane. It serves the
// three reads a sling pre-flight needs and refuses to be written to: any
// non-GET is recorded as a violation, which is how these tests prove that
// `gc sling --dry-run` against a remote city writes nothing.
type remoteSlingFake struct {
	t      *testing.T
	mu     sync.Mutex
	calls  []string // "<METHOD> <path>"
	beads  map[string]string
	status string
	rigs   string
}

func newRemoteSlingFake(t *testing.T, beadID, beadJSON, statusJSON, rigsJSON string) (*remoteSlingFake, *httptest.Server) {
	t.Helper()
	f := &remoteSlingFake{
		t:      t,
		beads:  map[string]string{beadID: beadJSON},
		status: statusJSON,
		rigs:   rigsJSON,
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *remoteSlingFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.mu.Unlock()

	if r.Method != http.MethodGet {
		f.t.Errorf("dry-run must not write: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	p := r.URL.Path
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(p, "/status"):
		_, _ = w.Write([]byte(f.status))
	case strings.HasSuffix(p, "/rigs"):
		_, _ = w.Write([]byte(f.rigs))
	case strings.HasPrefix(p, "/v0/city/mc/bead/"):
		// The single-bead read is /v0/city/{city}/bead/{id}.
		id := strings.TrimPrefix(p, "/v0/city/mc/bead/")
		body, ok := f.beads[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"bead not found"}`))
			return
		}
		_, _ = w.Write([]byte(body))
	default:
		f.t.Errorf("unexpected read path %s", p)
		w.WriteHeader(http.StatusNotFound)
	}
}

// callsFor returns the recorded call list (a copy).
func (f *remoteSlingFake) callsFor() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func remoteSlingBeadJSON(id, issueType, status, routedTo string) string {
	meta := `{}`
	if routedTo != "" {
		meta = `{"gc.routed_to":"` + routedTo + `"}`
	}
	return `{"id":"` + id + `","title":"a bead","status":"` + status +
		`","issue_type":"` + issueType + `","created_at":"2026-01-01T00:00:00Z","metadata":` + meta + `}`
}

const remoteSlingStatusCityPool = `{"name":"mc","path":"/city","agent_count":1,"agents":{},"rigs":{},"mail":{},"work":{},"uptime_sec":1,` +
	`"agent_details":[{"name":"mayor","qualified_name":"mayor","scope":"city","running":true,"suspended":false,"group_name":"mayor"},` +
	`{"name":"mechanic","qualified_name":"mechanic","scope":"city","running":false,"suspended":false,"group_name":"mechanic"}]}`

// A rig-scoped target: its qualified name carries the rig, and the rig table
// below gives that rig the "ws" bead prefix.
const remoteSlingStatusWithRigAgent = `{"name":"mc","path":"/city","agent_count":2,"agents":{},"rigs":{},"mail":{},"work":{},"uptime_sec":1,` +
	`"agent_details":[{"name":"mayor","qualified_name":"mayor","scope":"city","running":false,"suspended":false,"group_name":"mayor"},` +
	`{"name":"polecat","qualified_name":"west/polecat","scope":"rig","running":false,"suspended":false,"group_name":"west/polecat"}]}`

// The rigs list is a paged envelope: {"items":[...]}.
const remoteSlingRigs = `{"items":[{"name":"west","path":"/city/west","prefix":"ws","agent_count":1,"running_count":0,"suspended":false}]}`

// GREEN: the dry-run form prints the would-be route and never POSTs.
func TestCmdSlingRemoteDryRun_PreviewsAndWritesNothing(t *testing.T) {
	fake, srv := newRemoteSlingFake(t, "MC-1", remoteSlingBeadJSON("MC-1", "task", "open", "mechanic"), remoteSlingStatusCityPool, remoteSlingRigs)
	var out, errb bytes.Buffer

	code := cmdSlingRemote(remoteTestClient(t, srv.URL), remoteTestTarget(srv.URL), []string{"mayor", "MC-1"},
		false, false, false, "", nil, "", false, false, false, "", false, false, true /*dryRun*/, "", "", false, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d; stdout=%q stderr=%q", code, out.String(), errb.String())
	}
	got := out.String()
	for _, want := range []string{"mayor", "MC-1", "would write gc.routed_to=mayor", "No side effects executed (--dry-run)."} {
		if !strings.Contains(got, want) {
			t.Errorf("preview missing %q in %q", want, got)
		}
	}
	for _, call := range fake.callsFor() {
		if !strings.HasPrefix(call, "GET ") {
			t.Errorf("dry-run performed a non-read call: %s", call)
		}
	}
	// The bead's route key is byte-identical after the pre-flight: a fresh read
	// through the same client still returns the value it carried before.
	after, err := remoteTestClient(t, srv.URL).GetBead("MC-1")
	if err != nil {
		t.Fatalf("re-read failed: %v", err)
	}
	if got := after.Body.Metadata["gc.routed_to"]; got != "mechanic" {
		t.Errorf("gc.routed_to changed under the pre-flight: %q, want %q", got, "mechanic")
	}
}

// GREEN: --json emits the dry-run envelope (aligned with local `sling --json`
// keys: schema_version, success, dry_run, bead_id) and still never POSTs.
func TestCmdSlingRemoteDryRun_JSONEnvelope(t *testing.T) {
	fake, srv := newRemoteSlingFake(t, "MC-2", remoteSlingBeadJSON("MC-2", "bug", "open", ""), remoteSlingStatusCityPool, remoteSlingRigs)
	var out, errb bytes.Buffer

	code := cmdSlingRemote(remoteTestClient(t, srv.URL), remoteTestTarget(srv.URL), []string{"mayor", "MC-2"},
		false, false, false, "", nil, "", false, false, false, "", false, false, true /*dryRun*/, "", "", true /*json*/, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d; stderr=%q", code, errb.String())
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output not JSON: %v (%q)", err, out.String())
	}
	if got["schema_version"] != "1" || got["success"] != true || got["dry_run"] != true {
		t.Errorf("json = %v", got)
	}
	if got["bead_id"] != "MC-2" || got["target"] != "mayor" || got["would_set"] != "gc.routed_to=mayor" {
		t.Errorf("json = %v", got)
	}
	if nv, ok := got["not_verified"].([]any); !ok || len(nv) == 0 {
		t.Errorf("json must state what the pre-flight cannot check: %v", got)
	}
	for _, call := range fake.callsFor() {
		if !strings.HasPrefix(call, "GET ") {
			t.Errorf("dry-run performed a non-read call: %s", call)
		}
	}
}

// GREEN: the store agreement a real sling enforces (a cross-store route silently
// wedges pools — tr-6s7yx) is reported by the pre-flight, and reported as the
// refusal the real route would print.
func TestCmdSlingRemoteDryRun_RefusesCrossStore(t *testing.T) {
	fake, srv := newRemoteSlingFake(t, "MC-3", remoteSlingBeadJSON("MC-3", "task", "open", ""), remoteSlingStatusWithRigAgent, remoteSlingRigs)
	var out, errb bytes.Buffer

	code := cmdSlingRemote(remoteTestClient(t, srv.URL), remoteTestTarget(srv.URL), []string{"west/polecat", "MC-3"},
		false, false, false, "", nil, "", false, false, false, "", false, false, true /*dryRun*/, "", "", false, &out, &errb)
	if code != 1 {
		t.Fatalf("exit %d, want 1 (cross-store); stdout=%q stderr=%q", code, out.String(), errb.String())
	}
	for _, want := range []string{"refusing cross-store route", "city:mc", "west/polecat", "tr-6s7yx"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("stderr missing %q: %q", want, errb.String())
		}
	}
	for _, call := range fake.callsFor() {
		if !strings.HasPrefix(call, "GET ") {
			t.Errorf("the refusal still performed a non-read call: %s", call)
		}
	}
}

// A city-scoped target is cross-store eligible, so a city-store bead is a legal
// route to it: the pre-flight must not invent a refusal.
func TestCmdSlingRemoteDryRun_CityScopedTargetIsCrossStoreEligible(t *testing.T) {
	fake, srv := newRemoteSlingFake(t, "MC-4", remoteSlingBeadJSON("MC-4", "task", "open", ""), remoteSlingStatusWithRigAgent, remoteSlingRigs)
	var out, errb bytes.Buffer

	code := cmdSlingRemote(remoteTestClient(t, srv.URL), remoteTestTarget(srv.URL), []string{"mayor", "MC-4"},
		false, false, false, "", nil, "", false, false, false, "", false, false, true /*dryRun*/, "", "", false, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d; stdout=%q stderr=%q", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "store: city:mc") {
		t.Errorf("preview did not name the resolved store: %q", out.String())
	}
	for _, call := range fake.callsFor() {
		if !strings.HasPrefix(call, "GET ") {
			t.Errorf("dry-run performed a non-read call: %s", call)
		}
	}
}

// A rig-prefix bead to a target in that same rig is legal on both axes.
func TestCmdSlingRemoteDryRun_RigBeadToRigTargetIsLegal(t *testing.T) {
	fake, srv := newRemoteSlingFake(t, "WS-7", remoteSlingBeadJSON("WS-7", "task", "open", ""), remoteSlingStatusWithRigAgent, remoteSlingRigs)
	var out, errb bytes.Buffer

	code := cmdSlingRemote(remoteTestClient(t, srv.URL), remoteTestTarget(srv.URL), []string{"west/polecat", "WS-7"},
		false, false, false, "", nil, "", false, false, false, "", false, false, true /*dryRun*/, "", "", false, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d; stdout=%q stderr=%q", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "store: rig:west") {
		t.Errorf("preview did not resolve the rig store: %q", out.String())
	}
	for _, call := range fake.callsFor() {
		if !strings.HasPrefix(call, "GET ") {
			t.Errorf("dry-run performed a non-read call: %s", call)
		}
	}
}

// Class gate: an epic is not slingable (the same rule the server applies).
func TestCmdSlingRemoteDryRun_RefusesEpic(t *testing.T) {
	fake, srv := newRemoteSlingFake(t, "MC-5", remoteSlingBeadJSON("MC-5", "epic", "open", ""), remoteSlingStatusCityPool, remoteSlingRigs)
	var out, errb bytes.Buffer

	code := cmdSlingRemote(remoteTestClient(t, srv.URL), remoteTestTarget(srv.URL), []string{"mayor", "MC-5"},
		false, false, false, "", nil, "", false, false, false, "", false, false, true /*dryRun*/, "", "", false, &out, &errb)
	if code != 1 {
		t.Fatalf("exit %d, want 1 (epic); stdout=%q", code, out.String())
	}
	if !strings.Contains(errb.String(), "is an epic") {
		t.Errorf("stderr = %q", errb.String())
	}
	for _, call := range fake.callsFor() {
		if !strings.HasPrefix(call, "GET ") {
			t.Errorf("dry-run performed a non-read call: %s", call)
		}
	}
}

// Target validation happens against the REMOTE city: a name that is not one of
// its agents or pools is refused before anything is routed.
func TestCmdSlingRemoteDryRun_RefusesUnknownTarget(t *testing.T) {
	fake, srv := newRemoteSlingFake(t, "MC-6", remoteSlingBeadJSON("MC-6", "task", "open", ""), remoteSlingStatusCityPool, remoteSlingRigs)
	var out, errb bytes.Buffer

	code := cmdSlingRemote(remoteTestClient(t, srv.URL), remoteTestTarget(srv.URL), []string{"not-a-lane", "MC-6"},
		false, false, false, "", nil, "", false, false, false, "", false, false, true /*dryRun*/, "", "", false, &out, &errb)
	if code != 1 {
		t.Fatalf("exit %d, want 1 (unknown target); stdout=%q", code, out.String())
	}
	if !strings.Contains(errb.String(), "not an agent or pool") {
		t.Errorf("stderr = %q", errb.String())
	}
	for _, call := range fake.callsFor() {
		if !strings.HasPrefix(call, "GET ") {
			t.Errorf("dry-run performed a non-read call: %s", call)
		}
	}
}

// A bead already routed to the target is reported as the no-op the real sling
// would perform — read off the bead's own gc.routed_to.
func TestCmdSlingRemoteDryRun_ReportsIdempotentNoOp(t *testing.T) {
	fake, srv := newRemoteSlingFake(t, "MC-7", remoteSlingBeadJSON("MC-7", "task", "open", "mayor"), remoteSlingStatusCityPool, remoteSlingRigs)
	var out, errb bytes.Buffer

	code := cmdSlingRemote(remoteTestClient(t, srv.URL), remoteTestTarget(srv.URL), []string{"mayor", "MC-7"},
		false, false, false, "", nil, "", false, false, false, "", false, false, true /*dryRun*/, "", "", false, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d; stdout=%q stderr=%q", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "already routed to mayor") {
		t.Errorf("preview did not report the idempotent no-op: %q", out.String())
	}
	if strings.Contains(out.String(), "would write gc.routed_to") {
		t.Errorf("preview promised a write the real sling would not perform: %q", out.String())
	}
	for _, call := range fake.callsFor() {
		if !strings.HasPrefix(call, "GET ") {
			t.Errorf("dry-run performed a non-read call: %s", call)
		}
	}
}

// A container (convoy) is slingable, but its child expansion is server-side; the
// preview labels it rather than implying a single-bead route.
func TestCmdSlingRemoteDryRun_LabelsContainerBead(t *testing.T) {
	fake, srv := newRemoteSlingFake(t, "MC-8", remoteSlingBeadJSON("MC-8", "convoy", "open", ""), remoteSlingStatusCityPool, remoteSlingRigs)
	var out, errb bytes.Buffer

	code := cmdSlingRemote(remoteTestClient(t, srv.URL), remoteTestTarget(srv.URL), []string{"mayor", "MC-8"},
		false, false, false, "", nil, "", false, false, false, "", false, false, true /*dryRun*/, "", "", false, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d; stdout=%q stderr=%q", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "container") {
		t.Errorf("preview did not label the container: %q", out.String())
	}
	for _, call := range fake.callsFor() {
		if !strings.HasPrefix(call, "GET ") {
			t.Errorf("dry-run performed a non-read call: %s", call)
		}
	}
}

// Formula launches keep their refusal, and refuse it without even reading: there
// is nothing on this control plane that can preview a workflow launch.
func TestCmdSlingRemoteDryRun_FormulaRefusedWithoutReading(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("formula dry-run must refuse before touching the wire, read: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	var out, errb bytes.Buffer
	code := cmdSlingRemote(remoteTestClient(t, srv.URL), remoteTestTarget(srv.URL), []string{"mayor", "review"},
		true /*formula*/, false, false, "", []string{"pr=42"}, "", false, false, false, "", false, false, true /*dryRun*/, "", "", false, &out, &errb)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stdout=%q", code, out.String())
	}
	if !strings.Contains(errb.String(), "--dry-run with a formula") {
		t.Errorf("stderr = %q", errb.String())
	}
}

// A bead the remote city cannot read is a refusal, not a green preview: the real
// sling would fail its bead-existence check server-side.
func TestCmdSlingRemoteDryRun_RefusesUnreadableBead(t *testing.T) {
	fake, srv := newRemoteSlingFake(t, "MC-9", remoteSlingBeadJSON("MC-9", "task", "open", ""), remoteSlingStatusCityPool, remoteSlingRigs)
	var out, errb bytes.Buffer

	code := cmdSlingRemote(remoteTestClient(t, srv.URL), remoteTestTarget(srv.URL), []string{"mayor", "MC-GONE"},
		false, false, false, "", nil, "", false, false, false, "", false, false, true /*dryRun*/, "", "", false, &out, &errb)
	if code != 1 {
		t.Fatalf("exit %d, want 1 (unreadable bead); stdout=%q", code, out.String())
	}
	if !strings.Contains(errb.String(), "could not read bead MC-GONE") {
		t.Errorf("stderr = %q", errb.String())
	}
	for _, call := range fake.callsFor() {
		if !strings.HasPrefix(call, "GET ") {
			t.Errorf("dry-run performed a non-read call: %s", call)
		}
	}
}
