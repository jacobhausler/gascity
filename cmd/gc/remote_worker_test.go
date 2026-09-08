package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gastownhall/gascity/internal/clientcontext"
)

// remoteWorkerServer is a stand-in city that records what the CLI asked it for.
// The point of these tests is the ROUTING decision — which leg ran and what it
// sent — so the responses are the smallest shape the client accepts.
type remoteWorkerServer struct {
	*httptest.Server
	mu      sync.Mutex
	paths   []string
	updates []map[string]any
	// current is what GET /worker/current answers with.
	current string
	// claimStatus is the status POST /worker/claim answers with (200 by default).
	claimStatus int
	// listed is the beads GET /beads answers with.
	listed []map[string]any
}

func newRemoteWorkerServer(t *testing.T) *remoteWorkerServer {
	t.Helper()
	s := &remoteWorkerServer{claimStatus: http.StatusOK}
	mux := http.NewServeMux()
	record := func(next func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			s.mu.Lock()
			s.paths = append(s.paths, r.Method+" "+r.URL.Path)
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			next(w, r)
		}
	}
	mux.HandleFunc("/v0/city/mc/worker/current", record(func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		cur := s.current
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"bead_id": cur})
	}))
	mux.HandleFunc("/v0/city/mc/worker/claim", record(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "released"})
			return
		}
		s.mu.Lock()
		status := s.claimStatus
		s.mu.Unlock()
		if status != http.StatusOK {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"title": "Conflict", "status": status, "detail": "held"})
			return
		}
		var body struct {
			BeadID string `json:"bead_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "claimed",
			"bead":   map[string]any{"id": body.BeadID, "title": "work", "status": "in_progress", "assignee": "worker-1"},
		})
	}))
	mux.HandleFunc("/v0/city/mc/worker/heartbeat", record(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "renewed", "lease_scope": "bead-metadata"})
	}))
	mux.HandleFunc("/v0/city/mc/worker/drain-ack", record(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "acknowledged"})
	}))
	mux.HandleFunc("/v0/city/mc/worker/close", record(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "closed"})
	}))
	mux.HandleFunc("/v0/city/mc/worker/comment", record(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "commented"})
	}))
	mux.HandleFunc("/v0/city/mc/beads", record(func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		listed := s.listed
		s.mu.Unlock()
		if listed == nil {
			listed = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": listed})
	}))
	mux.HandleFunc("/v0/city/mc/bead/", record(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v0/city/mc/bead/"), "/update")
		if strings.HasSuffix(r.URL.Path, "/update") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.mu.Lock()
			s.updates = append(s.updates, body)
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "updated"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "title": "work", "status": "in_progress", "assignee": "worker-1"})
	}))
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func (s *remoteWorkerServer) lastUpdate() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.updates) == 0 {
		return nil
	}
	return s.updates[len(s.updates)-1]
}

func (s *remoteWorkerServer) sawPath(want string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.paths {
		if p == want {
			return true
		}
	}
	return false
}

// useRemoteWorkerContext points the CLI at the stand-in city through a named
// context, the same way an operator (or an in-alloc agent) would.
func useRemoteWorkerContext(t *testing.T, srv *remoteWorkerServer) {
	t.Helper()
	t.Setenv("GC_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if code := doContextAdd(clientcontext.Context{Name: "remote", URL: srv.URL, City: "mc"}, &out, &errb); code != 0 {
		t.Fatalf("seed context: %q", errb.String())
	}
	prev := contextFlag
	contextFlag = "remote"
	t.Cleanup(func() { contextFlag = prev })
}

// newRemoteWorkerLocalCity writes the smallest thing that resolves as a local
// city, so the local-target assertion exercises the resolver rather than the
// test binary's refusal to discover a city by walking upward.
func newRemoteWorkerLocalCity(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "city.toml"), []byte("[workspace]\nname = \"local\"\n"), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}
	return dir
}

// With no remote context configured, the resolver reports a LOCAL target, so
// every worker command runs the local path it always ran.
func TestResolveWorkerTargetLocalByDefault(t *testing.T) {
	t.Setenv("GC_HOME", t.TempDir())
	t.Setenv("GC_CITY_PATH", newRemoteWorkerLocalCity(t))
	client, isRemote, err := resolveWorkerTarget()
	if err != nil {
		t.Fatalf("resolveWorkerTarget: %v", err)
	}
	if isRemote || client != nil {
		t.Fatalf("want a local target, got isRemote=%v client=%v", isRemote, client != nil)
	}
}

func TestRemoteHookCurrentReadsTheAPI(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	srv.current = "mc-42"
	useRemoteWorkerContext(t, srv)

	client, isRemote, err := resolveWorkerTarget()
	if err != nil || !isRemote {
		t.Fatalf("want a remote target: isRemote=%v err=%v", isRemote, err)
	}
	var out, errb bytes.Buffer
	if code := remoteHookCurrent(client, "sess-1", true, &out, &errb); code != 0 {
		t.Fatalf("remoteHookCurrent = %d, stderr=%q", code, errb.String())
	}
	if got := strings.TrimSpace(out.String()); got != "mc-42" {
		t.Fatalf("stdout = %q, want mc-42", got)
	}
	if !srv.sawPath("GET /v0/city/mc/worker/current") {
		t.Fatalf("worker/current was not called; saw %v", srv.paths)
	}
}

// The local exit contract is preserved: an unclaimed session exits 1 rather
// than printing nothing and succeeding.
func TestRemoteHookCurrentExitsNonZeroWhenUnclaimed(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	useRemoteWorkerContext(t, srv)

	client, _, err := resolveWorkerTarget()
	if err != nil {
		t.Fatalf("resolveWorkerTarget: %v", err)
	}
	var out, errb bytes.Buffer
	if code := remoteHookCurrent(client, "sess-1", true, &out, &errb); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Fatalf("stdout = %q, want empty", out.String())
	}
	if !strings.Contains(errb.String(), "no current claim") {
		t.Fatalf("stderr = %q", errb.String())
	}
}

// A won claim emits the same result schema the local leg emits, so a startup
// wrapper cannot tell which leg produced its line.
func TestRemoteHookClaimClaimsThroughTheAPI(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	srv.listed = []map[string]any{{"id": "mc-7", "title": "routed", "status": "open", "metadata": map[string]string{"gc.routed_to": "worker-1"}}}
	useRemoteWorkerContext(t, srv)
	t.Setenv("GC_SESSION_ID", "sess-1")
	t.Setenv("GC_ALIAS", "worker-1")

	client, _, err := resolveWorkerTarget()
	if err != nil {
		t.Fatalf("resolveWorkerTarget: %v", err)
	}
	var out, errb bytes.Buffer
	if code := remoteHookClaim(client, hookCommandOptions{Claim: true, JSON: true}, &out, &errb); code != 0 {
		t.Fatalf("remoteHookClaim = %d, stderr=%q", code, errb.String())
	}
	var result hookClaimJSONResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &result); err != nil {
		t.Fatalf("decoding %q: %v", out.String(), err)
	}
	if result.Action != "work" || result.Reason != "claimed" || result.BeadID != "mc-7" {
		t.Fatalf("result = %+v", result)
	}
	if !srv.sawPath("POST /v0/city/mc/worker/claim") {
		t.Fatalf("worker/claim was not called; saw %v", srv.paths)
	}
}

// No routed work is a drain, and --drain-ack acknowledges it over the wire and
// exits 0 — the same contract the local leg has.
func TestRemoteHookClaimDrainsAndAcknowledges(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	useRemoteWorkerContext(t, srv)
	t.Setenv("GC_SESSION_ID", "sess-1")
	t.Setenv("GC_ALIAS", "worker-1")

	client, _, err := resolveWorkerTarget()
	if err != nil {
		t.Fatalf("resolveWorkerTarget: %v", err)
	}
	var out, errb bytes.Buffer
	code := remoteHookClaim(client, hookCommandOptions{Claim: true, DrainAck: true, JSON: true}, &out, &errb)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, errb.String())
	}
	var result hookClaimJSONResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &result); err != nil {
		t.Fatalf("decoding %q: %v", out.String(), err)
	}
	if result.Action != "drain" || !result.DrainAcknowledged {
		t.Fatalf("result = %+v", result)
	}
	if !srv.sawPath("POST /v0/city/mc/worker/drain-ack") {
		t.Fatalf("drain-ack was not called; saw %v", srv.paths)
	}
}

// A lost race is not an error: the claim conflicts, no candidate remains, and
// the hook drains rather than failing.
func TestRemoteHookClaimConflictFallsThroughToDrain(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	srv.claimStatus = http.StatusConflict
	srv.listed = []map[string]any{{"id": "mc-7", "title": "routed", "status": "open", "metadata": map[string]string{"gc.routed_to": "worker-1"}}}
	useRemoteWorkerContext(t, srv)
	t.Setenv("GC_SESSION_ID", "sess-1")
	t.Setenv("GC_ALIAS", "worker-1")

	client, _, err := resolveWorkerTarget()
	if err != nil {
		t.Fatalf("resolveWorkerTarget: %v", err)
	}
	var out, errb bytes.Buffer
	if code := remoteHookClaim(client, hookCommandOptions{Claim: true, JSON: true}, &out, &errb); code != 1 {
		t.Fatalf("code = %d, want 1 (drain without --drain-ack)", code)
	}
	var result hookClaimJSONResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &result); err != nil {
		t.Fatalf("decoding %q: %v", out.String(), err)
	}
	if result.Action != "drain" || result.Reason != hookClaimReasonNoWork {
		t.Fatalf("result = %+v", result)
	}
}

func TestRemoteHookClaimRefusesWithoutSessionIdentity(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	useRemoteWorkerContext(t, srv)
	t.Setenv("GC_SESSION_ID", "")

	client, _, err := resolveWorkerTarget()
	if err != nil {
		t.Fatalf("resolveWorkerTarget: %v", err)
	}
	var out, errb bytes.Buffer
	if code := remoteHookClaim(client, hookCommandOptions{Claim: true}, &out, &errb); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "GC_SESSION_ID") {
		t.Fatalf("stderr = %q", errb.String())
	}
}

// The worker `gc bd` subset routes to the API; everything else is reported
// unhandled so the caller emits the existing remote refusal.
func TestRemoteBdRoutesTheWorkerSubset(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	useRemoteWorkerContext(t, srv)
	client, _, err := resolveWorkerTarget()
	if err != nil {
		t.Fatalf("resolveWorkerTarget: %v", err)
	}

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"show", []string{"show", "mc-7"}, "GET /v0/city/mc/bead/mc-7"},
		{"comment", []string{"comment", "mc-7", "a", "note"}, "POST /v0/city/mc/worker/comment"},
		{"update", []string{"update", "mc-7", "--status", "in_progress"}, "POST /v0/city/mc/bead/mc-7/update"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code, handled := remoteBd(client, tc.args, &out, &errb)
			if !handled || code != 0 {
				t.Fatalf("handled=%v code=%d stderr=%q", handled, code, errb.String())
			}
			if !srv.sawPath(tc.want) {
				t.Fatalf("%s not called; saw %v", tc.want, srv.paths)
			}
		})
	}
}

func TestRemoteBdLeavesOtherVerbsUnhandled(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	useRemoteWorkerContext(t, srv)
	client, _, err := resolveWorkerTarget()
	if err != nil {
		t.Fatalf("resolveWorkerTarget: %v", err)
	}
	var out, errb bytes.Buffer
	if _, handled := remoteBd(client, []string{"sql", "select 1"}, &out, &errb); handled {
		t.Fatal("gc bd sql must not be routed to a remote city")
	}
}

// A city older than the worker route family answers 404 for every route in it.
// From inside the worker that is indistinguishable from a missing session, so
// the diagnostic has to say the other thing it might be.
func TestRemoteWorkerHintsWhenTheCityHasNoWorkerRoutes(t *testing.T) {
	// A city that serves nothing: every worker path is a router 404.
	old := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(old.Close)

	t.Setenv("GC_HOME", t.TempDir())
	var seedOut, seedErr bytes.Buffer
	if code := doContextAdd(clientcontext.Context{Name: "old", URL: old.URL, City: "mc"}, &seedOut, &seedErr); code != 0 {
		t.Fatalf("seed context: %q", seedErr.String())
	}
	prev := contextFlag
	contextFlag = "old"
	t.Cleanup(func() { contextFlag = prev })

	client, _, err := resolveWorkerTarget()
	if err != nil {
		t.Fatalf("resolveWorkerTarget: %v", err)
	}
	var out, errb bytes.Buffer
	if code := remoteHookCurrent(client, "sess-1", false, &out, &errb); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "worker route family") {
		t.Fatalf("stderr = %q, want the older-city hint", errb.String())
	}
}

// bd takes its flags on either side of the bead id, so this leg must too: an
// invocation that plainly names a bead must not come back "a bead id is
// required" because a flag was typed first.
func TestRemoteBdAcceptsFlagsBeforeTheBeadID(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	useRemoteWorkerContext(t, srv)
	client, _, err := resolveWorkerTarget()
	if err != nil {
		t.Fatalf("resolveWorkerTarget: %v", err)
	}

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"show --json first", []string{"show", "--json", "mc-7"}, "GET /v0/city/mc/bead/mc-7"},
		{"update value flag first", []string{"update", "--status", "in_progress", "mc-7"}, "POST /v0/city/mc/bead/mc-7/update"},
		{"update inline value first", []string{"update", "--status=in_progress", "mc-7"}, "POST /v0/city/mc/bead/mc-7/update"},
		{"update short flag first", []string{"update", "-s", "in_progress", "mc-7"}, "POST /v0/city/mc/bead/mc-7/update"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code, handled := remoteBd(client, tc.args, &out, &errb)
			if !handled || code != 0 {
				t.Fatalf("handled=%v code=%d stderr=%q", handled, code, errb.String())
			}
			if !srv.sawPath(tc.want) {
				t.Fatalf("%s not called; saw %v", tc.want, srv.paths)
			}
		})
	}
}

// `--json` before the id must still mean --json, not be swallowed as the id.
func TestRemoteBdShowJSONFlagWorksOnEitherSide(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	useRemoteWorkerContext(t, srv)
	client, _, err := resolveWorkerTarget()
	if err != nil {
		t.Fatalf("resolveWorkerTarget: %v", err)
	}
	for _, args := range [][]string{
		{"show", "--json", "mc-7"},
		{"show", "mc-7", "--json"},
	} {
		var out, errb bytes.Buffer
		if code, handled := remoteBd(client, args, &out, &errb); !handled || code != 0 {
			t.Fatalf("%v: handled=%v code=%d stderr=%q", args, handled, code, errb.String())
		}
		if !strings.HasPrefix(strings.TrimSpace(out.String()), "{") {
			t.Fatalf("%v printed %q, want JSON", args, out.String())
		}
	}
}

// close and show refuse an unknown flag for the same reason update does: the
// wire form cannot express it, and dropping it would answer a different question
// than the operator asked.
func TestRemoteBdRefusesUnknownFlagsOnEveryVerb(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	useRemoteWorkerContext(t, srv)
	client, _, err := resolveWorkerTarget()
	if err != nil {
		t.Fatalf("resolveWorkerTarget: %v", err)
	}

	for _, tc := range []struct {
		name    string
		args    []string
		refused string
	}{
		{"show", []string{"show", "mc-7", "--tree"}, "GET /v0/city/mc/bead/mc-7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code, handled := remoteBd(client, tc.args, &out, &errb)
			if !handled || code == 0 {
				t.Fatalf("handled=%v code=%d, want a refusal", handled, code)
			}
			if !strings.Contains(errb.String(), "not supported against a remote city") {
				t.Fatalf("stderr = %q", errb.String())
			}
			if srv.sawPath(tc.refused) {
				t.Fatalf("a refused %s still reached the city; saw %v", tc.name, srv.paths)
			}
		})
	}
}

// Comment text is not flag-parsed: a note that starts with a dash is a note.
func TestRemoteBdCommentTakesItsTextVerbatim(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	useRemoteWorkerContext(t, srv)
	client, _, err := resolveWorkerTarget()
	if err != nil {
		t.Fatalf("resolveWorkerTarget: %v", err)
	}
	var out, errb bytes.Buffer
	code, handled := remoteBd(client, []string{"comment", "mc-7", "--", "a", "note"}, &out, &errb)
	if !handled || code != 0 {
		t.Fatalf("handled=%v code=%d stderr=%q", handled, code, errb.String())
	}
	if !srv.sawPath("POST /v0/city/mc/worker/comment") {
		t.Fatalf("comment not called; saw %v", srv.paths)
	}
}

// An update flag the wire form cannot express is refused, never dropped: a
// half-applied update is worse than a failed one.
func TestRemoteBdUpdateRefusesUnsupportedFlags(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	useRemoteWorkerContext(t, srv)
	client, _, err := resolveWorkerTarget()
	if err != nil {
		t.Fatalf("resolveWorkerTarget: %v", err)
	}
	var out, errb bytes.Buffer
	code, handled := remoteBd(client, []string{"update", "mc-7", "--set-metadata", "k=v"}, &out, &errb)
	if !handled || code == 0 {
		t.Fatalf("handled=%v code=%d, want a refusal", handled, code)
	}
	if !strings.Contains(errb.String(), "not supported against a remote city") {
		t.Fatalf("stderr = %q", errb.String())
	}
	if srv.sawPath("POST /v0/city/mc/bead/mc-7/update") {
		t.Fatal("a refused update must not reach the city")
	}
}

func TestRemoteBdUpdateAcceptsFormulaStepMetadata(t *testing.T) {
	for _, outcome := range []string{"pass", "fail"} {
		t.Run(outcome, func(t *testing.T) {
			srv := newRemoteWorkerServer(t)
			useRemoteWorkerContext(t, srv)
			client, _, err := resolveWorkerTarget()
			if err != nil {
				t.Fatalf("resolveWorkerTarget: %v", err)
			}

			var out, errb bytes.Buffer
			code, handled := remoteBd(client, []string{
				"update", "formula-step",
				"--set-metadata", "gc.outcome=" + outcome,
				"--set-metadata", "gc.step_id=implement",
				"--set-metadata", "gc.step_ref=westlands-verified-work.implement",
				"--set-metadata", "gc.step_timeout=5m",
				"--status", "closed",
			}, &out, &errb)
			if !handled || code != 0 {
				t.Fatalf("handled=%v code=%d stderr=%q", handled, code, errb.String())
			}
			// The remote route takes the positional bead identifier verbatim. It
			// does not resolve a title/name locally, which keeps name-vs-id
			// behavior explicit for callers using an id-shaped formula-step ref.
			if !srv.sawPath("POST /v0/city/mc/bead/formula-step/update") {
				t.Fatalf("update route not called for the supplied bead id; saw %v", srv.paths)
			}
			body := srv.lastUpdate()
			if body["status"] != "closed" {
				t.Fatalf("status = %#v, want closed", body["status"])
			}
			metadata, ok := body["metadata"].(map[string]any)
			if !ok {
				t.Fatalf("metadata = %#v, want an object", body["metadata"])
			}
			want := map[string]string{
				"gc.outcome":      outcome,
				"gc.step_id":      "implement",
				"gc.step_ref":     "westlands-verified-work.implement",
				"gc.step_timeout": "5m",
			}
			for key, value := range want {
				if metadata[key] != value {
					t.Errorf("metadata[%q] = %#v, want %q", key, metadata[key], value)
				}
			}
		})
	}
}

func TestRemoteBdUpdateRefusesInvalidFormulaOutcomeBeforeWrite(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	useRemoteWorkerContext(t, srv)
	client, _, err := resolveWorkerTarget()
	if err != nil {
		t.Fatalf("resolveWorkerTarget: %v", err)
	}
	for _, metadata := range []string{"gc.outcome=done", "gc.step_unknown=value"} {
		t.Run(metadata, func(t *testing.T) {
			var out, errb bytes.Buffer
			code, handled := remoteBd(client, []string{
				"update", "mc-7", "--set-metadata", metadata,
			}, &out, &errb)
			if !handled || code == 0 {
				t.Fatalf("handled=%v code=%d, want a refusal", handled, code)
			}
			if !strings.Contains(errb.String(), strings.Split(metadata, "=")[0]) {
				t.Fatalf("stderr = %q, want metadata-key refusal", errb.String())
			}
			if srv.sawPath("POST /v0/city/mc/bead/mc-7/update") {
				t.Fatal("invalid formula metadata must not reach the city")
			}
		})
	}
}

// The identity ladder is alias, then agent, then session name, then session id
// — most specific first, and deduplicated.
func TestRemoteWorkerIdentitiesOrder(t *testing.T) {
	t.Setenv("GC_ALIAS", "myrig/worker-3")
	t.Setenv("GC_AGENT", "myrig/worker-3")
	t.Setenv("GC_SESSION_NAME", "worker-3")
	t.Setenv("GC_TEMPLATE", "worker")
	t.Setenv("GC_RIG", "myrig")
	got := remoteWorkerIdentities("sess-1")
	want := []string{"myrig/worker-3", "worker-3", "sess-1", "myrig/worker", "worker"}
	if len(got) != len(want) {
		t.Fatalf("identities = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("identities = %v, want %v", got, want)
		}
	}
}

// A remote pool worker carries the bare template in GC_TEMPLATE, while the
// city's routed work may use the rig-qualified identity stamped by gc sling.
// The remote leg must build the same route target set as the local leg.
func TestRemoteHookCandidatesSelectsRigQualifiedTemplateRoute(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	srv.listed = []map[string]any{{
		"id": "mc-template", "title": "template-routed", "status": "open",
		"metadata": map[string]string{"gc.routed_to": "myrig/worker"},
	}}
	useRemoteWorkerContext(t, srv)
	t.Setenv("GC_ALIAS", "myrig/worker-3")
	t.Setenv("GC_AGENT", "myrig/worker-3")
	t.Setenv("GC_SESSION_NAME", "worker-3")
	t.Setenv("GC_TEMPLATE", "worker")
	t.Setenv("GC_RIG", "myrig")

	client, _, err := resolveWorkerTarget()
	if err != nil {
		t.Fatalf("resolveWorkerTarget: %v", err)
	}
	var errb bytes.Buffer
	got, errored := remoteHookCandidates(client, remoteWorkerIdentities("sess-1"), &errb)
	if errored {
		t.Fatalf("candidates reported an error; stderr=%q", errb.String())
	}
	if len(got) != 1 || got[0].ID != "mc-template" {
		t.Fatalf("candidates = %+v, want template-routed bead; stderr=%q", got, errb.String())
	}
}

// Gas City routes work by stamping gc.routed_to and leaving assignee null, so
// routed-open work is what a remote worker must see. The earlier
// `?assignee=<identity>` list matched the assignee COLUMN and returned nothing
// for such a bead, so an in-alloc worker drained with no_work while the same
// bead was plainly claimable on the local leg (DF-08 L2 gap 2).
func TestRemoteHookCandidatesSelectRoutedOpenWork(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	srv.listed = []map[string]any{
		{"id": "mc-routed", "title": "routed", "status": "open", "metadata": map[string]string{"gc.routed_to": "myrig/worker"}},
		{"id": "mc-assigned", "title": "direct", "status": "open", "assignee": "myrig/worker"},
		{"id": "mc-elsewhere", "title": "someone else's", "status": "open", "metadata": map[string]string{"gc.routed_to": "myrig/other"}},
		{"id": "mc-held", "title": "held", "status": "open", "assignee": "myrig/other", "metadata": map[string]string{"gc.routed_to": "myrig/worker"}},
		{"id": "mc-mail", "title": "mail", "status": "open", "issue_type": "message", "metadata": map[string]string{"gc.routed_to": "myrig/worker"}},
		{"id": "mc-fallback", "title": "bare template", "status": "open", "metadata": map[string]string{"gc.routed_to": "myrig/template"}},
	}
	useRemoteWorkerContext(t, srv)

	client, _, err := resolveWorkerTarget()
	if err != nil {
		t.Fatalf("resolveWorkerTarget: %v", err)
	}
	var errb bytes.Buffer
	got, errored := remoteHookCandidates(client, []string{"myrig/worker", "myrig/template"}, &errb)
	if errored {
		t.Fatalf("candidates reported an error; stderr=%q", errb.String())
	}
	var ids []string
	for _, b := range got {
		ids = append(ids, b.ID)
	}
	// Identity order first (the suffixed identity before its bare template),
	// and nothing routed elsewhere, held by another worker, or mail.
	want := []string{"mc-routed", "mc-assigned", "mc-fallback"}
	if len(ids) != len(want) {
		t.Fatalf("candidates = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", ids, want)
		}
	}
}

// remoteCityUnderTest points the CLI's ad-hoc remote tier at srv for the
// duration of one test. It sets the flag globals the same way
// cmd/gc/capstone_integration_test.go:51-53 does — that is the resolution path
// `gc --city-url … --city-name …` takes (targetFromURL, remote_target.go:292-296:
// an ad-hoc target REQUIRES --city-name, and carries only a GC_CITY_URL_TOKEN
// bearer, never a context) — and restores them afterwards so a leaked global
// cannot make an unrelated test resolve remote.
func remoteCityUnderTest(t *testing.T, srv *remoteWorkerServer) {
	t.Helper()
	origCtx, origURL, origName := contextFlag, cityURLFlag, cityNameFlag
	t.Cleanup(func() { contextFlag, cityURLFlag, cityNameFlag = origCtx, origURL, origName })
	contextFlag, cityURLFlag, cityNameFlag = "", srv.URL, "mc"
	t.Setenv("GC_HOME", t.TempDir())
	// The claim leg names its claimant from the environment on the local path
	// (cmd_bd_by_id.go:1296-1300); the remote leg must honor the same identity
	// rather than the transport identity, so the tests set it the way a pool
	// session does.
	t.Setenv("BEADS_ACTOR", "worker-local-3-pool")
}

// TestDoBd_ClaimUnderRemoteTarget_ReachesTheWorkerRoute is the load-bearing RED
// line: `gc bd update <id> --claim` (in-process by-ID arm at
// cmd_bd_by_id.go:187 → graph.Claim :1302) must, given a remote target, travel
// as POST /v0/city/{city}/worker/claim instead of failing the capability gate
// or exec'ing bd.
func TestDoBd_ClaimUnderRemoteTarget_ReachesTheWorkerRoute(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	remoteCityUnderTest(t, srv)

	var stdout, stderr bytes.Buffer
	code := doBd([]string{"update", "cr-gdeav.5.3", "--claim"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("remote claim exit = %d, want 0; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	want := "POST /v0/city/mc/worker/claim"
	if !srv.sawPath(want) {
		t.Fatalf("no request reached %s; requests seen: %v", want, srv.paths)
	}
}

// TestDoBd_HeartbeatUnderRemoteTarget_ReachesTheWorkerRoute: today heartbeat is
// a passthrough to bd's own verb (rewriteBdHeartbeatArgs, cmd_bd.go:198-211),
// which is precisely the case an off-host worker cannot use — bd is not running
// where the worker is, and gc holds no lease table for it.
func TestDoBd_HeartbeatUnderRemoteTarget_ReachesTheWorkerRoute(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	remoteCityUnderTest(t, srv)

	var stdout, stderr bytes.Buffer
	code := doBd([]string{"heartbeat", "cr-gdeav.5.3"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("remote heartbeat exit = %d, want 0; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	want := "POST /v0/city/mc/worker/heartbeat"
	if !srv.sawPath(want) {
		t.Fatalf("no request reached %s; requests seen: %v", want, srv.paths)
	}
}

// TestDoBd_ReleaseIfCurrentUnderRemoteTarget_UsesDeleteOnTheClaimResource
// keeps the CLI on the same route the edge family already chose (release is
// DELETE /worker/claim, and the local verb already takes the expected assignee
// positionally: parseBdReleaseIfCurrentArgs, cmd_bd.go:600-608).
func TestDoBd_ReleaseIfCurrentUnderRemoteTarget_UsesDeleteOnTheClaimResource(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	remoteCityUnderTest(t, srv)

	var stdout, stderr bytes.Buffer
	code := doBd([]string{"release-if-current", "cr-gdeav.5.3", "worker-local-3-pool"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("remote release exit = %d, want 0; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	want := "DELETE /v0/city/mc/worker/claim"
	if !srv.sawPath(want) {
		t.Fatalf("no request reached %s; requests seen: %v", want, srv.paths)
	}
}

// TestDoBd_WorkerSubsetKeepsTheLocalJSONRowSchemaAndExitCode: the startup
// wrapper parses this row and cannot tell a local claim from a remote one, so
// the remote leg must answer with the same shape and the same 0.
func TestDoBd_WorkerSubsetKeepsTheLocalJSONRowSchemaAndExitCode(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	remoteCityUnderTest(t, srv)

	var stdout, stderr bytes.Buffer
	code := doBd([]string{"update", "cr-gdeav.5.3", "--claim", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("remote claim exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	row := strings.TrimSpace(stdout.String())
	var parsed map[string]any
	if err := json.Unmarshal([]byte(row), &parsed); err != nil {
		t.Fatalf("remote leg must emit the same JSON row the local leg does; got %q (%v)", row, err)
	}
	for _, key := range []string{"id", "status", "assignee"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("remote claim row missing %q that the local by-ID claim carries; row=%v", key, parsed)
		}
	}
}

func TestParseBdWorkerCloseRefusesUnknownMetadata(t *testing.T) {
	for _, pair := range []string{"gc.routed_to=worker-local-3-pool", "malformed"} {
		if _, err := parseBdWorkerCloseArgs([]string{"mc-7", "--set-metadata", pair}); err == nil {
			t.Fatalf("metadata pair %q was silently discarded", pair)
		}
	}
}

// A workflow root carries gc.run_target rather than gc.routed_to, and the local
// leg treats it as routed work; the remote leg must agree, because the two
// share hookClaimMatchesRoute.
func TestRemoteHookCandidatesAcceptWorkflowRunTarget(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	srv.listed = []map[string]any{{
		"id": "mc-flow", "title": "workflow root", "status": "open",
		"metadata": map[string]string{"gc.kind": "workflow", "gc.run_target": "myrig/worker"},
	}}
	useRemoteWorkerContext(t, srv)

	client, _, err := resolveWorkerTarget()
	if err != nil {
		t.Fatalf("resolveWorkerTarget: %v", err)
	}
	var errb bytes.Buffer
	got, errored := remoteHookCandidates(client, []string{"myrig/worker"}, &errb)
	if errored || len(got) != 1 || got[0].ID != "mc-flow" {
		t.Fatalf("candidates = %+v errored=%v stderr=%q", got, errored, errb.String())
	}
}

// `gc bd` disables flag parsing, so cobra never fills the root's --city-name
// for it and the ad-hoc tier (GC_CITY_URL + --city-name) was refused outright:
// an in-alloc worker could reach `gc hook` from its environment but not
// `gc bd`, which needed a named context (DF-08 L2 gap 1). Both tiers must work.
func TestDoBdReachesARemoteCityOnEitherTier(t *testing.T) {
	t.Run("ad-hoc --city-name with GC_CITY_URL", func(t *testing.T) {
		srv := newRemoteWorkerServer(t)
		t.Setenv("GC_HOME", t.TempDir())
		t.Setenv("GC_CITY_URL", srv.URL)
		var out, errb bytes.Buffer
		if code := doBd([]string{"show", "mc-7", "--city-name", "mc"}, &out, &errb); code != 0 {
			t.Fatalf("doBd = %d, stderr=%q", code, errb.String())
		}
		if !srv.sawPath("GET /v0/city/mc/bead/mc-7") {
			t.Fatalf("bead read not routed to the remote city; saw %v", srv.paths)
		}
	})

	t.Run("ad-hoc --city-url=/--city-name= spelling", func(t *testing.T) {
		srv := newRemoteWorkerServer(t)
		t.Setenv("GC_HOME", t.TempDir())
		var out, errb bytes.Buffer
		code := doBd([]string{"--city-url=" + srv.URL, "--city-name=mc", "show", "mc-7"}, &out, &errb)
		if code != 0 {
			t.Fatalf("doBd = %d, stderr=%q", code, errb.String())
		}
		if !srv.sawPath("GET /v0/city/mc/bead/mc-7") {
			t.Fatalf("bead read not routed to the remote city; saw %v", srv.paths)
		}
	})

	t.Run("named context", func(t *testing.T) {
		srv := newRemoteWorkerServer(t)
		useRemoteWorkerContext(t, srv)
		var out, errb bytes.Buffer
		if code := doBd([]string{"show", "mc-7"}, &out, &errb); code != 0 {
			t.Fatalf("doBd = %d, stderr=%q", code, errb.String())
		}
		if !srv.sawPath("GET /v0/city/mc/bead/mc-7") {
			t.Fatalf("bead read not routed to the remote city; saw %v", srv.paths)
		}
	})
}

// The extracted selectors are gc's, not bd's: leaving them in the argument list
// would hand bd two unknown flags on the local path.
func TestExtractBdRemoteFlagsRemovesThemFromTheBdArgs(t *testing.T) {
	url, name, rest := extractBdRemoteFlags([]string{"--city-url", "https://city.example", "show", "--city-name=mc", "mc-7"})
	if url != "https://city.example" || name != "mc" {
		t.Fatalf("url=%q name=%q", url, name)
	}
	if strings.Join(rest, " ") != "show mc-7" {
		t.Fatalf("rest = %v", rest)
	}
}

// TestDoBd_NonWorkerVerb_StillRefusesAndNamesTheSubset is the negative half of
// the whitelist at the top of doBd (cmd_bd.go:317): passthrough verbs cannot be
// remote, and the refusal must say which verbs ARE available instead of the
// generic incremental-enablement text (remote_target.go:135-137).
func TestDoBd_NonWorkerVerb_StillRefusesAndNamesTheSubset(t *testing.T) {
	srv := newRemoteWorkerServer(t)
	remoteCityUnderTest(t, srv)

	var stdout, stderr bytes.Buffer
	code := doBd([]string{"list"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("gc bd list under a remote target must refuse, got exit 0; stdout=%q", stdout.String())
	}
	msg := stdout.String() + stderr.String()
	if !strings.Contains(msg, "worker") {
		t.Fatalf("refusal must name the worker subset the remote tier does support; got %q", msg)
	}
	if len(srv.paths) != 0 {
		t.Fatalf("a refused verb must not reach the city at all; requests: %v", srv.paths)
	}
}
