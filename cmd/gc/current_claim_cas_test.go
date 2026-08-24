package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// currentClaimCASTestStore is the smallest stateful session-pointer seam for
// hook tests. It deliberately implements value CAS rather than a mutex around a
// read followed by SetMetadata: the production invariant is that only one
// same-session claimant can reserve a different bead.
type currentClaimCASTestStore struct {
	mu          sync.Mutex
	pointer     string
	reserveErr  error
	clearErr    error
	reserveHook func()
	clearHook   func()
	readHook    func()
}

func (s *currentClaimCASTestStore) read(string) (string, error) {
	if s.readHook != nil {
		s.readHook()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pointer, nil
}

func (s *currentClaimCASTestStore) reserve(_ string, expected, next string) (beads.MetadataCASOutcome, error) {
	if s.reserveHook != nil {
		s.reserveHook()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reserveErr != nil {
		return "", s.reserveErr
	}
	if s.pointer != expected {
		return beads.MetadataCASConflict, nil
	}
	s.pointer = next
	return beads.MetadataCASSwapped, nil
}

func (s *currentClaimCASTestStore) clear(_ string, expected string) (beads.MetadataCASOutcome, error) {
	if s.clearHook != nil {
		s.clearHook()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clearErr != nil {
		return "", s.clearErr
	}
	if s.pointer != expected {
		return beads.MetadataCASConflict, nil
	}
	s.pointer = ""
	return beads.MetadataCASSwapped, nil
}

func currentClaimTestOps(store *currentClaimCASTestStore, row string, claimedCalls, stampCalls, releaseCalls *atomic.Int32) hookClaimOps {
	return hookClaimOps{
		Runner:              func(string, string) (string, error) { return row, nil },
		ReadSessionClaim:    store.read,
		ReserveSessionClaim: store.reserve,
		ClearSessionClaim:   store.clear,
		Claim: func(_ context.Context, _ string, _ []string, id, assignee string) (beads.Bead, bool, error) {
			claimedCalls.Add(1)
			return beads.Bead{ID: id, Status: "in_progress", Assignee: assignee, Metadata: map[string]string{"gc.routed_to": "worker"}}, true, nil
		},
		StampWorkMeta: func(context.Context, string, []string, string, string, map[string]string) error {
			stampCalls.Add(1)
			return nil
		},
		ReadWorkMeta: func(_ context.Context, _ string, _ []string, id, assignee string) (beads.Bead, error) {
			return beads.Bead{ID: id, Status: "in_progress", Assignee: assignee, Metadata: map[string]string{"gc.session_id": "s-1"}}, nil
		},
		PublishRunMap: noopPublishRunMap,
		Release: func(_ context.Context, _ string, _ []string, _, _ string) (bool, error) {
			releaseCalls.Add(1)
			return true, nil
		},
		EmitClaimReleased: func(hookClaimReleaseRecord) {},
	}
}

func currentClaimTestOpts() hookClaimOptions {
	return hookClaimOptions{
		Assignee:     "worker-1",
		RouteTargets: []string{"worker"},
		Env:          []string{"GC_SESSION_ID=s-1", "GC_SESSION_NAME=worker-1"},
		JSON:         true,
	}
}

// TestTwoHooksCASReserveOneWinnerAndReleaseOnlyLoser proves the end-to-end
// same-session different-bead fence: both work claims may win their own work
// CAS, but only one can reserve the session pointer. The loser releases only
// its minted work and emits no work/drain output; the sole output's bead is the
// durable current claim.
func TestTwoHooksCASReserveOneWinnerAndReleaseOnlyLoser(t *testing.T) {
	store := &currentClaimCASTestStore{}
	var mu sync.Mutex
	outputs := make(map[string]string, 1)
	codes := make(map[string]int, 2)
	var releaseCalls, stampCalls, claimedCalls atomic.Int32
	start := make(chan struct{})
	readReady := make(chan struct{}, 2)
	readRelease := make(chan struct{})
	store.readHook = func() {
		readReady <- struct{}{}
		<-readRelease
	}
	var wg sync.WaitGroup
	for _, id := range []string{"work-a", "work-b"} {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ops := currentClaimTestOps(store, `[ {"id":"`+id+`","status":"open","metadata":{"gc.routed_to":"worker"}} ]`, &claimedCalls, &stampCalls, &releaseCalls)
			var stdout, stderr bytes.Buffer
			code := doHookClaim("query", "/work", currentClaimTestOpts(), ops, &stdout, &stderr)
			if code != 0 && code != 1 {
				t.Errorf("hook %s code = %d, want winner 0 or loser 1; stderr=%s", id, code, stderr.String())
			}
			mu.Lock()
			codes[id] = code
			if stdout.Len() > 0 {
				outputs[id] = strings.TrimSpace(stdout.String())
			}
			mu.Unlock()
		}()
	}
	close(start)
	<-readReady
	<-readReady
	close(readRelease)
	wg.Wait()

	if len(outputs) != 1 {
		t.Fatalf("outputs = %v, want exactly one winner output", outputs)
	}
	if len(codes) != 2 {
		t.Fatalf("codes = %v, want both hook outcomes", codes)
	}
	winnerID := ""
	for id, code := range codes {
		want := 1
		if _, ok := outputs[id]; ok {
			want = 0
			winnerID = id
		}
		if code != want {
			t.Fatalf("hook %s code = %d, want deterministic %d (output=%q)", id, code, want, outputs[id])
		}
	}
	if winnerID == "" {
		t.Fatal("no hook produced the winner output")
	}
	if got := releaseCalls.Load(); got != 1 {
		t.Fatalf("release calls = %d, want exactly one loser release", got)
	}
	if got := stampCalls.Load(); got != 1 {
		t.Fatalf("work metadata stamps = %d, want only the winner to continue downstream", got)
	}
	if got := claimedCalls.Load(); got != 2 {
		t.Fatalf("work claims = %d, want both minted claims before pointer CAS", got)
	}
	store.mu.Lock()
	pointer := store.pointer
	store.mu.Unlock()
	var result hookClaimJSONResult
	winnerOutput := outputs[winnerID]
	if err := json.Unmarshal([]byte(winnerOutput), &result); err != nil {
		t.Fatalf("winner output is not JSON: %v (%q)", err, winnerOutput)
	}
	if pointer != result.BeadID {
		t.Fatalf("current pointer = %q, want sole output bead %q", pointer, result.BeadID)
	}
}

// TestCurrentClaimCASConflictHasNoDownstreamEffects proves a pointer conflict
// is fail-closed: the minted work is released, but no work metadata/events,
// continuation, output, or drain path runs.
func TestCurrentClaimCASConflictHasNoDownstreamEffects(t *testing.T) {
	store := &currentClaimCASTestStore{pointer: "winner"}
	var claimedCalls, stampCalls, releaseCalls atomic.Int32
	ops := currentClaimTestOps(store, `[{"id":"loser","status":"open","metadata":{"gc.routed_to":"worker"}}]`, &claimedCalls, &stampCalls, &releaseCalls)
	ops.ReadWorkMeta = func(_ context.Context, _ string, _ []string, id, assignee string) (beads.Bead, error) {
		if id == "winner" {
			return beads.Bead{}, beads.ErrNotFound
		}
		return beads.Bead{ID: id, Status: "in_progress", Assignee: assignee, Metadata: map[string]string{"gc.session_id": "s-1"}}, nil
	}
	ops.ReserveSessionClaim = func(string, string, string) (beads.MetadataCASOutcome, error) {
		return beads.MetadataCASConflict, nil
	}
	var stdout, stderr bytes.Buffer
	code := doHookClaim("query", "/work", currentClaimTestOpts(), ops, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("code = 0, want conflict failure; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no work/drain output", stdout.String())
	}
	if claimedCalls.Load() != 1 || releaseCalls.Load() != 1 || stampCalls.Load() != 0 {
		t.Fatalf("calls = claimed:%d released:%d stamped:%d, want 1/1/0", claimedCalls.Load(), releaseCalls.Load(), stampCalls.Load())
	}
	if store.pointer != "winner" {
		t.Fatalf("conflict changed winner pointer to %q", store.pointer)
	}
}

// TestCurrentClaimCASUnsupportedHasNoUnconditionalFallback pins the capability
// fence: a missing/unsupported reservation is an operational failure, never a
// fallback to SetCurrentClaim followed by downstream work output.
func TestCurrentClaimCASUnsupportedHasNoUnconditionalFallback(t *testing.T) {
	store := &currentClaimCASTestStore{reserveErr: beads.ErrConditionalWriteUnsupported}
	var claimedCalls, stampCalls, releaseCalls atomic.Int32
	ops := currentClaimTestOps(store, `[{"id":"work","status":"open","metadata":{"gc.routed_to":"worker"}}]`, &claimedCalls, &stampCalls, &releaseCalls)
	var stdout, stderr bytes.Buffer
	if code := doHookClaim("query", "/work", currentClaimTestOpts(), ops, &stdout, &stderr); code == 0 {
		t.Fatalf("code = 0, want unsupported-CAS failure; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || claimedCalls.Load() != 1 || releaseCalls.Load() != 1 || stampCalls.Load() != 0 {
		t.Fatalf("unsupported CAS effects = stdout:%q claimed:%d released:%d stamped:%d, want empty/1/1/0", stdout.String(), claimedCalls.Load(), releaseCalls.Load(), stampCalls.Load())
	}
}

// TestCurrentClaimCASAmbiguousReserveDoesNotRelease proves a transport error
// after the CAS may have committed: the minted work must remain parked for
// reconciliation, with no release, result, or drain that could strand a
// pointer to work this invocation has already given back.
func TestCurrentClaimCASAmbiguousReserveDoesNotRelease(t *testing.T) {
	store := &currentClaimCASTestStore{reserveErr: errors.New("metadata CAS transport interrupted")}
	var claimedCalls, stampCalls, releaseCalls atomic.Int32
	ops := currentClaimTestOps(store, `[{
		"id":"work-ambiguous","status":"open","metadata":{"gc.routed_to":"worker"}
	}]`, &claimedCalls, &stampCalls, &releaseCalls)
	var stdout, stderr bytes.Buffer
	code := doHookClaim("query", "/work", currentClaimTestOpts(), ops, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("code = 0, want ambiguous-CAS failure; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no result or drain", stdout.String())
	}
	if releaseCalls.Load() != 0 {
		t.Fatalf("release calls = %d, want 0 for commit-ambiguous CAS", releaseCalls.Load())
	}
	if stampCalls.Load() != 0 || claimedCalls.Load() != 1 {
		t.Fatalf("calls = claimed:%d stamped:%d, want 1/0", claimedCalls.Load(), stampCalls.Load())
	}
}

// TestCurrentClaimCASCommittedThenErrorKeepsMintedWork proves a reserve error
// remains ambiguous even when the backing pointer has already changed. The
// minted work must stay parked because releasing it could leave the session
// pointer naming work this invocation no longer owns.
func TestCurrentClaimCASCommittedThenErrorKeepsMintedWork(t *testing.T) {
	store := &currentClaimCASTestStore{reserveErr: errors.New("reserve reply lost")}
	store.reserveHook = func() {
		store.mu.Lock()
		store.pointer = "work-committed"
		store.mu.Unlock()
	}
	var claimedCalls, stampCalls, releaseCalls, reserveCalls, drainCalls atomic.Int32
	ops := currentClaimTestOps(store, `[{"id":"work-committed","status":"open","metadata":{"gc.routed_to":"worker"}}]`, &claimedCalls, &stampCalls, &releaseCalls)
	ops.ReserveSessionClaim = func(sessionID, expected, next string) (beads.MetadataCASOutcome, error) {
		reserveCalls.Add(1)
		return store.reserve(sessionID, expected, next)
	}
	ops.DrainAck = func(io.Writer) error {
		drainCalls.Add(1)
		return nil
	}
	var stdout, stderr bytes.Buffer
	if code := doHookClaim("query", "/work", currentClaimTestOpts(), ops, &stdout, &stderr); code == 0 {
		t.Fatalf("code = 0, want ambiguous reserve failure; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || releaseCalls.Load() != 0 || drainCalls.Load() != 0 {
		t.Fatalf("effects = stdout:%q releases:%d drains:%d, want empty/0/0", stdout.String(), releaseCalls.Load(), drainCalls.Load())
	}
	if claimedCalls.Load() != 1 || reserveCalls.Load() != 1 || stampCalls.Load() != 0 {
		t.Fatalf("calls = claimed:%d reserved:%d stamped:%d, want 1/1/0", claimedCalls.Load(), reserveCalls.Load(), stampCalls.Load())
	}
	store.mu.Lock()
	got := store.pointer
	store.mu.Unlock()
	if got != "work-committed" {
		t.Fatalf("pointer = %q, want committed minted claim retained", got)
	}
}

// TestCurrentClaimCASReversedCandidateAdoptsPointerFirst prevents candidate
// ordering from selecting a different already-owned bead when the session's
// current pointer identifies a live assignment later in the query result.
func TestCurrentClaimCASReversedCandidateAdoptsPointerFirst(t *testing.T) {
	store := &currentClaimCASTestStore{pointer: "work-b"}
	var claimedCalls, stampCalls, releaseCalls atomic.Int32
	ops := currentClaimTestOps(store, `[
        {"id":"work-a","status":"in_progress","assignee":"worker-1","metadata":{"gc.routed_to":"worker"}},
        {"id":"work-b","status":"in_progress","assignee":"worker-1","metadata":{"gc.routed_to":"worker"}}
	]`, &claimedCalls, &stampCalls, &releaseCalls)
	ops.Claim = func(context.Context, string, []string, string, string) (beads.Bead, bool, error) {
		claimedCalls.Add(1)
		return beads.Bead{}, false, errors.New("pointer adoption must not claim")
	}
	var stdout, stderr bytes.Buffer
	if code := doHookClaim("query", "/work", currentClaimTestOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, want pointer adoption success; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "work-b") {
		t.Fatalf("stdout = %q, want current pointer work-b", stdout.String())
	}
	if claimedCalls.Load() != 0 || releaseCalls.Load() != 0 {
		t.Fatalf("adoption calls = claimed:%d released:%d, want 0/0", claimedCalls.Load(), releaseCalls.Load())
	}
}

// TestCurrentClaimCASStalePointerAllowsExpectedOldReplacement proves a
// terminal pointer is resolved authoritatively before fresh work is claimed;
// only then may the fresh claim replace the stale expected value.
func TestCurrentClaimCASStalePointerAllowsExpectedOldReplacement(t *testing.T) {
	store := &currentClaimCASTestStore{pointer: "old"}
	var claimedCalls, stampCalls, releaseCalls atomic.Int32
	ops := currentClaimTestOps(store, `[{"id":"new","status":"open","metadata":{"gc.routed_to":"worker"}}]`, &claimedCalls, &stampCalls, &releaseCalls)
	ops.ReadWorkMeta = func(_ context.Context, _ string, _ []string, id, _ string) (beads.Bead, error) {
		if id == "old" {
			return beads.Bead{ID: id, Status: "closed", Assignee: "worker-1"}, nil
		}
		return beads.Bead{ID: id, Status: "in_progress", Assignee: "worker-1", Metadata: map[string]string{"gc.session_id": "s-1"}}, nil
	}
	var stdout, stderr bytes.Buffer
	if code := doHookClaim("query", "/work", currentClaimTestOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, want fresh replacement success; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "\"bead_id\":\"new\"") {
		t.Fatalf("stdout = %q, want replacement bead new", stdout.String())
	}
	if claimedCalls.Load() != 1 || releaseCalls.Load() != 0 {
		t.Fatalf("calls = claimed:%d released:%d, want 1/0", claimedCalls.Load(), releaseCalls.Load())
	}
	store.mu.Lock()
	got := store.pointer
	store.mu.Unlock()
	if got != "new" {
		t.Fatalf("pointer = %q, want expected-old→new replacement", got)
	}
}

// TestCurrentClaimCASLivePointerBlocksDifferentFreshClaim proves a live
// current pointer is adopted even when it is absent from the ready-query rows;
// the session cannot claim a different bead in the same invocation.
func TestCurrentClaimCASLivePointerBlocksDifferentFreshClaim(t *testing.T) {
	store := &currentClaimCASTestStore{pointer: "live"}
	var claimedCalls, stampCalls, releaseCalls atomic.Int32
	ops := currentClaimTestOps(store, `[{"id":"fresh","status":"open","metadata":{"gc.routed_to":"worker"}}]`, &claimedCalls, &stampCalls, &releaseCalls)
	var stdout, stderr bytes.Buffer
	if code := doHookClaim("query", "/work", currentClaimTestOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, want live-pointer adoption success; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "\"bead_id\":\"live\"") {
		t.Fatalf("stdout = %q, want live pointer bead", stdout.String())
	}
	if claimedCalls.Load() != 0 || releaseCalls.Load() != 0 {
		t.Fatalf("calls = claimed:%d released:%d, want 0/0", claimedCalls.Load(), releaseCalls.Load())
	}
}

// TestCurrentClaimCASEmptyQueryAdoptsLivePointer proves the authoritative
// current pointer is resolved even when the ready query has no rows. A live
// assignment must be returned as work, never converted into a no-work drain.
func TestCurrentClaimCASEmptyQueryAdoptsLivePointer(t *testing.T) {
	store := &currentClaimCASTestStore{pointer: "live"}
	var claimedCalls, stampCalls, releaseCalls atomic.Int32
	ops := currentClaimTestOps(store, `[]`, &claimedCalls, &stampCalls, &releaseCalls)
	var stdout, stderr bytes.Buffer
	code := doHookClaim("query", "/work", currentClaimTestOpts(), ops, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want live-pointer adoption success; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"bead_id":"live"`) {
		t.Fatalf("stdout = %q, want live pointer bead", stdout.String())
	}
	if claimedCalls.Load() != 0 || releaseCalls.Load() != 0 || stampCalls.Load() != 1 {
		t.Fatalf("calls = claimed:%d released:%d stamped:%d, want 0/0/1", claimedCalls.Load(), releaseCalls.Load(), stampCalls.Load())
	}
}

// TestCurrentClaimCASEpipeClearCannotEraseLaterWinner proves the undelivered
// result path clears conditionally: when another same-session winner replaces
// the pointer before the clear, the winner remains and only this invocation's
// work claim is released.
func TestCurrentClaimCASEpipeClearCannotEraseLaterWinner(t *testing.T) {
	store := &currentClaimCASTestStore{}
	store.clearHook = func() {
		store.mu.Lock()
		store.pointer = "winner"
		store.mu.Unlock()
	}
	var claimedCalls, stampCalls, releaseCalls atomic.Int32
	ops := currentClaimTestOps(store, `[{"id":"loser","status":"open","metadata":{"gc.routed_to":"worker"}}]`, &claimedCalls, &stampCalls, &releaseCalls)
	var stderr bytes.Buffer
	code := doHookClaim("query", "/work", currentClaimTestOpts(), ops, brokenPipeWriter{}, &stderr)
	if code != 1 {
		t.Fatalf("code = %d, want EPIPE failure", code)
	}
	if claimedCalls.Load() != 1 || releaseCalls.Load() != 1 {
		t.Fatalf("calls = claimed:%d released:%d, want 1/1", claimedCalls.Load(), releaseCalls.Load())
	}
	store.mu.Lock()
	got := store.pointer
	store.mu.Unlock()
	if got != "winner" {
		t.Fatalf("EPIPE clear erased later winner: pointer = %q", got)
	}
}

// TestCurrentClaimCASEpipeClearAmbiguousDoesNotRelease proves a clear error
// leaves ownership unresolved: the minted work cannot be released while the
// session pointer may still advertise it.
func TestCurrentClaimCASEpipeClearAmbiguousDoesNotRelease(t *testing.T) {
	store := &currentClaimCASTestStore{clearErr: errors.New("clear transport interrupted")}
	var claimedCalls, stampCalls, releaseCalls atomic.Int32
	ops := currentClaimTestOps(store, `[{"id":"loser","status":"open","metadata":{"gc.routed_to":"worker"}}]`, &claimedCalls, &stampCalls, &releaseCalls)
	var stderr bytes.Buffer
	code := doHookClaim("query", "/work", currentClaimTestOpts(), ops, brokenPipeWriter{}, &stderr)
	if code != 1 {
		t.Fatalf("code = %d, want EPIPE failure", code)
	}
	if releaseCalls.Load() != 0 {
		t.Fatalf("release calls = %d, want 0 after ambiguous clear", releaseCalls.Load())
	}
	if claimedCalls.Load() != 1 || stampCalls.Load() != 1 {
		t.Fatalf("calls = claimed:%d stamped:%d, want 1/1", claimedCalls.Load(), stampCalls.Load())
	}
}

// TestCurrentClaimCASEpipeClearCommittedThenErrorDoesNotRelease proves a clear
// error remains ambiguous even when the clear has already landed. Releasing is
// unsafe until ownership is reconciled, so the minted work remains parked.
func TestCurrentClaimCASEpipeClearCommittedThenErrorDoesNotRelease(t *testing.T) {
	store := &currentClaimCASTestStore{clearErr: errors.New("clear reply lost")}
	store.clearHook = func() {
		store.mu.Lock()
		store.pointer = ""
		store.mu.Unlock()
	}
	var claimedCalls, stampCalls, releaseCalls, clearCalls, drainCalls atomic.Int32
	ops := currentClaimTestOps(store, `[{"id":"loser","status":"open","metadata":{"gc.routed_to":"worker"}}]`, &claimedCalls, &stampCalls, &releaseCalls)
	ops.ClearSessionClaim = func(sessionID, expected string) (beads.MetadataCASOutcome, error) {
		clearCalls.Add(1)
		return store.clear(sessionID, expected)
	}
	ops.DrainAck = func(io.Writer) error {
		drainCalls.Add(1)
		return nil
	}
	var stderr bytes.Buffer
	if code := doHookClaim("query", "/work", currentClaimTestOpts(), ops, brokenPipeWriter{}, &stderr); code != 1 {
		t.Fatalf("code = %d, want EPIPE failure", code)
	}
	if releaseCalls.Load() != 0 || drainCalls.Load() != 0 {
		t.Fatalf("effects = released:%d drained:%d, want 0/0 after committed-but-ambiguous clear", releaseCalls.Load(), drainCalls.Load())
	}
	if claimedCalls.Load() != 1 || clearCalls.Load() != 1 || stampCalls.Load() != 1 {
		t.Fatalf("calls = claimed:%d cleared:%d stamped:%d, want 1/1/1", claimedCalls.Load(), clearCalls.Load(), stampCalls.Load())
	}
	store.mu.Lock()
	got := store.pointer
	store.mu.Unlock()
	if got != "" {
		t.Fatalf("pointer = %q, want clear committed before ambiguous error", got)
	}
}

// TestCurrentClaimCASReadFailureFailsClosed proves a current-pointer read
// error does not enter the work query, claim, drain, or output paths.
func TestCurrentClaimCASReadFailureFailsClosed(t *testing.T) {
	store := &currentClaimCASTestStore{}
	var claimedCalls, stampCalls, releaseCalls, queryCalls, reserveCalls, drainCalls atomic.Int32
	ops := currentClaimTestOps(store, `[{"id":"fresh","status":"open","metadata":{"gc.routed_to":"worker"}}]`, &claimedCalls, &stampCalls, &releaseCalls)
	ops.Runner = func(string, string) (string, error) {
		queryCalls.Add(1)
		return `[{"id":"fresh","status":"open"}]`, nil
	}
	ops.ReserveSessionClaim = func(sessionID, expected, next string) (beads.MetadataCASOutcome, error) {
		reserveCalls.Add(1)
		return store.reserve(sessionID, expected, next)
	}
	ops.DrainAck = func(io.Writer) error {
		drainCalls.Add(1)
		return nil
	}
	ops.ReadSessionClaim = func(string) (string, error) { return "", errors.New("current claim read failed") }
	var stdout, stderr bytes.Buffer
	if code := doHookClaim("query", "/work", currentClaimTestOpts(), ops, &stdout, &stderr); code != 1 {
		t.Fatalf("code = %d, want read failure", code)
	}
	if queryCalls.Load() != 0 || claimedCalls.Load() != 0 || reserveCalls.Load() != 0 || stampCalls.Load() != 0 || releaseCalls.Load() != 0 || drainCalls.Load() != 0 || stdout.Len() != 0 {
		t.Fatalf("read failure effects = runner:%d claimed:%d reserved:%d stamped:%d released:%d drained:%d stdout:%q; want all zero", queryCalls.Load(), claimedCalls.Load(), reserveCalls.Load(), stampCalls.Load(), releaseCalls.Load(), drainCalls.Load(), stdout.String())
	}
}

// TestCurrentClaimCASPointedReadFailureFailsClosed proves an error resolving
// the snapshotted current pointer does not enter fresh-work query or mutation,
// and emits neither a work result nor a no-work drain.
func TestCurrentClaimCASPointedReadFailureFailsClosed(t *testing.T) {
	store := &currentClaimCASTestStore{pointer: "live"}
	var claimedCalls, stampCalls, releaseCalls, queryCalls, workReadCalls, reserveCalls, drainCalls atomic.Int32
	ops := currentClaimTestOps(store, `[{"id":"fresh","status":"open","metadata":{"gc.routed_to":"worker"}}]`, &claimedCalls, &stampCalls, &releaseCalls)
	ops.Runner = func(string, string) (string, error) {
		queryCalls.Add(1)
		return `[{"id":"fresh","status":"open"}]`, nil
	}
	ops.ReserveSessionClaim = func(sessionID, expected, next string) (beads.MetadataCASOutcome, error) {
		reserveCalls.Add(1)
		return store.reserve(sessionID, expected, next)
	}
	ops.DrainAck = func(io.Writer) error {
		drainCalls.Add(1)
		return nil
	}
	ops.ReadWorkMeta = func(context.Context, string, []string, string, string) (beads.Bead, error) {
		workReadCalls.Add(1)
		return beads.Bead{}, errors.New("pointed work read failed")
	}
	var stdout, stderr bytes.Buffer
	if code := doHookClaim("query", "/work", currentClaimTestOpts(), ops, &stdout, &stderr); code != 1 {
		t.Fatalf("code = %d, want pointed-read failure", code)
	}
	if workReadCalls.Load() != 1 {
		t.Fatalf("pointed work reads = %d, want one authoritative read", workReadCalls.Load())
	}
	if queryCalls.Load() != 0 || claimedCalls.Load() != 0 || reserveCalls.Load() != 0 || stampCalls.Load() != 0 || releaseCalls.Load() != 0 || drainCalls.Load() != 0 || stdout.Len() != 0 {
		t.Fatalf("pointed read failure effects = runner:%d claimed:%d reserved:%d stamped:%d released:%d drained:%d stdout:%q; want runner/claim/reserve/stamp/release/drain/output all zero", queryCalls.Load(), claimedCalls.Load(), reserveCalls.Load(), stampCalls.Load(), releaseCalls.Load(), drainCalls.Load(), stdout.String())
	}
}

// TestClaimHookWorkWithRunnerAdoptsLiveCurrentBeforeStoreDiscovery proves the
// authoritative session pointer wins before federated store selection. A live
// assignment must be returned even when every store is empty, with no runner or
// drain path involved.
func TestClaimHookWorkWithRunnerAdoptsLiveCurrentBeforeStoreDiscovery(t *testing.T) {
	store := &currentClaimCASTestStore{pointer: "live"}
	stores := []hookStore{
		{dir: "city", env: []string{"GC_STORE=city"}},
		{dir: "riga", env: []string{"GC_STORE=riga"}},
	}
	var claimedCalls, stampCalls, releaseCalls, runnerCalls, drainCalls atomic.Int32
	ops := currentClaimTestOps(store, `[]`, &claimedCalls, &stampCalls, &releaseCalls)
	ops.ResolveWorkBranch = func(string) string { return "" }
	ops.DrainAck = func(io.Writer) error {
		drainCalls.Add(1)
		return nil
	}
	run := func(string, string, []string) (string, error) {
		runnerCalls.Add(1)
		return `[]`, nil
	}
	var stdout, stderr bytes.Buffer
	code := claimHookWorkWithRunner("query", "city", stores[0].env, stores, currentClaimTestOpts(), ops, run, func(string, error) {}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("claimHookWorkWithRunner = %d, want existing-assignment success; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var result hookClaimJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout.String())
	}
	if result.Action != "work" || result.Reason != "existing_assignment" || result.BeadID != "live" {
		t.Fatalf("result = %+v, want existing_assignment/live", result)
	}
	if runnerCalls.Load() != 0 || claimedCalls.Load() != 0 || releaseCalls.Load() != 0 || drainCalls.Load() != 0 {
		t.Fatalf("downstream calls = runner:%d claimed:%d stamped:%d released:%d drained:%d, want no runner/claim/release/drain", runnerCalls.Load(), claimedCalls.Load(), stampCalls.Load(), releaseCalls.Load(), drainCalls.Load())
	}
}

// TestClaimHookWorkWithRunnerPointedReadFailureFailsClosed proves a pointed
// assignment read error fails before any federated store runner or drain.
func TestClaimHookWorkWithRunnerPointedReadFailureFailsClosed(t *testing.T) {
	store := &currentClaimCASTestStore{pointer: "live"}
	stores := []hookStore{
		{dir: "city", env: []string{"GC_STORE=city"}},
		{dir: "riga", env: []string{"GC_STORE=riga"}},
	}
	var claimedCalls, stampCalls, releaseCalls, runnerCalls, reserveCalls, drainCalls atomic.Int32
	ops := currentClaimTestOps(store, `[]`, &claimedCalls, &stampCalls, &releaseCalls)
	ops.ReadWorkMeta = func(context.Context, string, []string, string, string) (beads.Bead, error) {
		return beads.Bead{}, errors.New("pointed work read transport failed")
	}
	ops.ReserveSessionClaim = func(sessionID, expected, next string) (beads.MetadataCASOutcome, error) {
		reserveCalls.Add(1)
		return store.reserve(sessionID, expected, next)
	}
	ops.ResolveWorkBranch = func(string) string { return "" }
	ops.DrainAck = func(io.Writer) error {
		drainCalls.Add(1)
		return nil
	}
	run := func(string, string, []string) (string, error) {
		runnerCalls.Add(1)
		return `[]`, nil
	}
	var stdout, stderr bytes.Buffer
	code := claimHookWorkWithRunner("query", "city", stores[0].env, stores, currentClaimTestOpts(), ops, run, func(string, error) {}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("claimHookWorkWithRunner = %d, want pointed-read failure; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if runnerCalls.Load() != 0 || claimedCalls.Load() != 0 || reserveCalls.Load() != 0 || stampCalls.Load() != 0 || releaseCalls.Load() != 0 || drainCalls.Load() != 0 || stdout.Len() != 0 {
		t.Fatalf("pointed read failure effects = runner:%d claimed:%d reserved:%d stamped:%d released:%d drained:%d stdout:%q; want all zero", runnerCalls.Load(), claimedCalls.Load(), reserveCalls.Load(), stampCalls.Load(), releaseCalls.Load(), drainCalls.Load(), stdout.String())
	}
}

// TestClaimHookWorkWithRunnerPromotesOpenCurrentBeforeStoreDiscovery proves an
// open pointer takes the canonical ready-assignment claim/readback path. It is
// promoted exactly once and reported as ready_assignment without querying or
// draining any federated store.
func TestClaimHookWorkWithRunnerPromotesOpenCurrentBeforeStoreDiscovery(t *testing.T) {
	store := &currentClaimCASTestStore{pointer: "open-current"}
	stores := []hookStore{
		{dir: "city", env: []string{"GC_STORE=city"}},
		{dir: "riga", env: []string{"GC_STORE=riga"}},
	}
	var claimedCalls, stampCalls, releaseCalls, runnerCalls, drainCalls atomic.Int32
	ops := currentClaimTestOps(store, `[]`, &claimedCalls, &stampCalls, &releaseCalls)
	ops.ReadWorkMeta = func(_ context.Context, _ string, _ []string, id, assignee string) (beads.Bead, error) {
		return beads.Bead{ID: id, Status: "open", Assignee: assignee, Metadata: map[string]string{"gc.routed_to": "worker"}}, nil
	}
	ops.Claim = func(_ context.Context, _ string, _ []string, id, assignee string) (beads.Bead, bool, error) {
		claimedCalls.Add(1)
		return beads.Bead{ID: id, Status: "in_progress", Assignee: assignee, Metadata: map[string]string{"gc.routed_to": "worker", "gc.session_id": "s-1"}}, true, nil
	}
	ops.ResolveWorkBranch = func(string) string { return "" }
	ops.DrainAck = func(io.Writer) error {
		drainCalls.Add(1)
		return nil
	}
	run := func(string, string, []string) (string, error) {
		runnerCalls.Add(1)
		return `[]`, nil
	}
	var stdout, stderr bytes.Buffer
	code := claimHookWorkWithRunner("query", "city", stores[0].env, stores, currentClaimTestOpts(), ops, run, func(string, error) {}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("claimHookWorkWithRunner = %d, want ready-assignment success; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var result hookClaimJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout.String())
	}
	if result.Action != "work" || result.Reason != "ready_assignment" || result.BeadID != "open-current" {
		t.Fatalf("result = %+v, want ready_assignment/open-current", result)
	}
	if claimedCalls.Load() != 1 || runnerCalls.Load() != 0 || stampCalls.Load() != 1 || releaseCalls.Load() != 0 || drainCalls.Load() != 0 {
		t.Fatalf("calls = claimed:%d runner:%d stamped:%d released:%d drained:%d, want 1/0/1/0/0", claimedCalls.Load(), runnerCalls.Load(), stampCalls.Load(), releaseCalls.Load(), drainCalls.Load())
	}
}

// TestCurrentClaimCASLaterWorkLegInProgressUsesItsExactContext proves that a
// pointer held by a later work leg is adopted before query selection. The
// session identity remains the invocation env, while work reads/writes use the
// resident leg's exact dir/env.
func TestCurrentClaimCASLaterWorkLegInProgressUsesItsExactContext(t *testing.T) {
	store := &currentClaimCASTestStore{pointer: "later-live"}
	stores := []hookStore{
		{dir: "rig-a", env: []string{"GC_STORE=rig-a"}},
		{dir: "city", env: []string{"GC_STORE=city"}},
	}
	bead := beads.Bead{ID: "later-live", Status: "in_progress", Assignee: "worker-1", Metadata: map[string]string{
		beadmeta.SessionIDMetadataKey:   "s-1",
		beadmeta.SessionNameMetadataKey: "worker-1",
	}}
	var reads, runnerCalls, drainCalls, stampCalls atomic.Int32
	var readContexts []hookStore
	var stampContext hookStore
	var resolverAssignee, stampAssignee string
	var snapshotSession string
	ops := currentClaimTestOps(store, `[]`, new(atomic.Int32), new(atomic.Int32), new(atomic.Int32))
	ops.ReadSessionClaim = func(sessionID string) (string, error) {
		snapshotSession = sessionID
		return store.read(sessionID)
	}
	ops.ReadWorkMeta = func(_ context.Context, dir string, env []string, id, assignee string) (beads.Bead, error) {
		reads.Add(1)
		resolverAssignee = assignee
		readContexts = append(readContexts, hookStore{dir: dir, env: append([]string(nil), env...)})
		if dir == "rig-a" {
			return beads.Bead{}, beads.ErrNotFound
		}
		if dir == "city" {
			return bead, nil
		}
		return beads.Bead{}, errors.New("unexpected work leg")
	}
	ops.ResolveWorkBranch = func(string) string { return "" }
	ops.StampWorkMeta = func(_ context.Context, dir string, env []string, _, assignee string, _ map[string]string) error {
		stampCalls.Add(1)
		stampAssignee = assignee
		stampContext = hookStore{dir: dir, env: append([]string(nil), env...)}
		return nil
	}
	ops.DrainAck = func(io.Writer) error { drainCalls.Add(1); return nil }
	run := func(string, string, []string) (string, error) { runnerCalls.Add(1); return `[]`, nil }
	var stdout, stderr bytes.Buffer
	code := claimHookWorkWithRunner("query", "rig-a", stores[0].env, stores, currentClaimTestOpts(), ops, run, func(string, error) {}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("claimHookWorkWithRunner = %d, want existing-assignment success; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if reads.Load() != 3 || len(readContexts) != 3 {
		t.Fatalf("reads = %d contexts=%v, want one exact read per ordered work leg plus resident canonical read", reads.Load(), readContexts)
	}
	if readContexts[0].dir != "rig-a" || !reflect.DeepEqual(readContexts[0].env, stores[0].env) ||
		readContexts[1].dir != "city" || !reflect.DeepEqual(readContexts[1].env, stores[1].env) ||
		readContexts[2].dir != "city" || !reflect.DeepEqual(readContexts[2].env, stores[1].env) {
		t.Fatalf("pointer contexts = %v, want rig-a/%v then city/%v", readContexts, stores[0].env, stores[1].env)
	}
	if runnerCalls.Load() != 0 || drainCalls.Load() != 0 {
		t.Fatalf("runner/drain = %d/%d, want 0/0 for live later-leg adoption", runnerCalls.Load(), drainCalls.Load())
	}
	if snapshotSession != "s-1" {
		t.Fatalf("session snapshot identity = %q, want s-1 while work selectors use the later leg", snapshotSession)
	}
	if resolverAssignee != currentClaimTestOpts().Assignee || stampAssignee != currentClaimTestOpts().Assignee {
		t.Fatalf("resolver/stamp assignees = %q/%q, want invocation assignee %q", resolverAssignee, stampAssignee, currentClaimTestOpts().Assignee)
	}
	if stampCalls.Load() != 1 || stampContext.dir != "city" || !reflect.DeepEqual(stampContext.env, stores[1].env) {
		t.Fatalf("stamp context = %d/%+v, want one city-leg write with exact env", stampCalls.Load(), stampContext)
	}
	var result hookClaimJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout.String())
	}
	if result.BeadID != bead.ID || result.Reason != "existing_assignment" {
		t.Fatalf("result = %+v, want existing_assignment/%s", result, bead.ID)
	}
}

// TestCurrentClaimCASLaterWorkLegOpenUsesItsExactContext proves an open
// pointer in a later leg is promoted exactly once, without querying or
// draining. The Claim seam must receive the resident leg context, not the
// invocation's session env.
func TestCurrentClaimCASLaterWorkLegOpenUsesItsExactContext(t *testing.T) {
	store := &currentClaimCASTestStore{pointer: "later-open"}
	stores := []hookStore{
		{dir: "rig-a", env: []string{"GC_STORE=rig-a"}},
		{dir: "city", env: []string{"GC_STORE=city"}},
	}
	open := beads.Bead{ID: "later-open", Status: "open", Assignee: "worker-canonical", Metadata: map[string]string{
		beadmeta.SessionIDMetadataKey: "s-1", beadmeta.SessionNameMetadataKey: "worker-canonical", beadmeta.ClaimedAtMetadataKey: "2026-08-24T00:00:00Z",
	}}
	var runnerCalls, drainCalls, claimCalls, releaseCalls atomic.Int32
	var claimContext hookStore
	var stampContext hookStore
	var releaseContext hookStore
	var resolverAssignee, claimActor, stampAssignee, releaseAssignee string
	var readCalls atomic.Int32
	ops := currentClaimTestOps(store, `[]`, new(atomic.Int32), new(atomic.Int32), new(atomic.Int32))
	ops.ReadWorkMeta = func(_ context.Context, dir string, env []string, id, assignee string) (beads.Bead, error) {
		readCalls.Add(1)
		resolverAssignee = assignee
		if dir == "rig-a" {
			return beads.Bead{}, beads.ErrNotFound
		}
		if dir == "city" {
			if readCalls.Load() == 2 {
				return open, nil
			}
			return beads.Bead{ID: id, Status: "in_progress", Assignee: "worker-canonical", Metadata: open.Metadata}, nil
		}
		return beads.Bead{}, errors.New("unexpected work leg")
	}
	ops.Claim = func(_ context.Context, dir string, env []string, id, assignee string) (beads.Bead, bool, error) {
		claimCalls.Add(1)
		claimActor = assignee
		claimContext = hookStore{dir: dir, env: append([]string(nil), env...)}
		return beads.Bead{ID: id, Status: "in_progress", Assignee: assignee, Metadata: open.Metadata}, true, nil
	}
	ops.StampWorkMeta = func(_ context.Context, dir string, env []string, _, assignee string, _ map[string]string) error {
		stampAssignee = assignee
		stampContext = hookStore{dir: dir, env: append([]string(nil), env...)}
		return nil
	}
	ops.Release = func(_ context.Context, dir string, env []string, _, assignee string) (bool, error) {
		releaseCalls.Add(1)
		releaseContext = hookStore{dir: dir, env: append([]string(nil), env...)}
		releaseAssignee = assignee
		return true, nil
	}
	ops.ResolveWorkBranch = func(string) string { return "" }
	ops.DrainAck = func(io.Writer) error { drainCalls.Add(1); return nil }
	run := func(string, string, []string) (string, error) { runnerCalls.Add(1); return `[]`, nil }
	opts := currentClaimTestOpts()
	opts.Assignee = "worker-invocation"
	opts.IdentityCandidates = []string{"worker-canonical"}
	var stdout, stderr bytes.Buffer
	code := claimHookWorkWithRunner("query", "rig-a", stores[0].env, stores, opts, ops, run, func(string, error) {}, brokenPipeWriter{}, &stderr)
	if code != 1 {
		t.Fatalf("claimHookWorkWithRunner = %d, want EPIPE failure after promotion; stderr=%q", code, stderr.String())
	}
	if claimCalls.Load() != 1 || runnerCalls.Load() != 0 || drainCalls.Load() != 0 || releaseCalls.Load() != 1 {
		t.Fatalf("claim/runner/drain/release = %d/%d/%d/%d, want 1/0/0/1", claimCalls.Load(), runnerCalls.Load(), drainCalls.Load(), releaseCalls.Load())
	}
	if claimContext.dir != stores[1].dir || !reflect.DeepEqual(claimContext.env, stores[1].env) {
		t.Fatalf("promotion context = %+v, want city/%v; session CAS identity must remain %v", claimContext, stores[1].env, opts.Env)
	}
	if resolverAssignee != opts.Assignee {
		t.Fatalf("resolver assignee = %q, want invocation assignee %q", resolverAssignee, opts.Assignee)
	}
	if claimActor != open.Assignee {
		t.Fatalf("open-promotion claim actor = %q, want canonical bead assignee %q", claimActor, open.Assignee)
	}
	if stampAssignee != opts.Assignee {
		t.Fatalf("stamp assignee = %q, want invocation assignee %q", stampAssignee, opts.Assignee)
	}
	if stampContext.dir != stores[1].dir || !reflect.DeepEqual(stampContext.env, stores[1].env) {
		t.Fatalf("stamp context = %+v, want city/%v", stampContext, stores[1].env)
	}
	if releaseContext.dir != stores[1].dir || !reflect.DeepEqual(releaseContext.env, stores[1].env) || releaseAssignee != open.Assignee {
		t.Fatalf("release context/actor = %+v/%q, want city/%v/%q", releaseContext, releaseAssignee, stores[1].env, open.Assignee)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no delivered result after EPIPE", stdout.String())
	}
}

// TestCurrentClaimCASLaterWorkLegContinuationUsesItsExactContext proves that
// an adopted pointer carries the resident leg through the continuation helpers
// as well as the claim/readback path. The actor remains the invocation actor:
// the resident bead's assignee is only the canonical actor for promotion and
// release of an open bead.
func TestCurrentClaimCASLaterWorkLegContinuationUsesItsExactContext(t *testing.T) {
	store := &currentClaimCASTestStore{pointer: "later-continuation"}
	stores := []hookStore{
		{dir: "rig-a", env: []string{"GC_STORE=rig-a"}},
		{dir: "city", env: []string{"GC_STORE=city"}},
	}
	bead := beads.Bead{ID: store.pointer, Status: "in_progress", Assignee: "worker-canonical", Metadata: map[string]string{
		beadmeta.SessionIDMetadataKey:         "s-1",
		beadmeta.SessionNameMetadataKey:       "worker-canonical",
		beadmeta.RootBeadIDMetadataKey:        "root-1",
		beadmeta.ContinuationGroupMetadataKey: "body",
	}}
	sibling := beads.Bead{ID: "sibling", Status: "open", Metadata: map[string]string{
		beadmeta.RoutedToMetadataKey:          "worker",
		beadmeta.RootBeadIDMetadataKey:        "root-1",
		beadmeta.ContinuationGroupMetadataKey: "body",
	}}
	var readContexts []hookStore
	var stampContext, listContext, assignContext hookStore
	var resolverAssignee, stampAssignee, assignAssignee string
	var listedRoot, listedGroup, assignedID string
	var reads, stampCalls, listCalls, assignCalls, runnerCalls, drainCalls atomic.Int32
	ops := currentClaimTestOps(store, `[]`, new(atomic.Int32), new(atomic.Int32), new(atomic.Int32))
	ops.ReadWorkMeta = func(_ context.Context, dir string, env []string, id, assignee string) (beads.Bead, error) {
		reads.Add(1)
		resolverAssignee = assignee
		readContexts = append(readContexts, hookStore{dir: dir, env: append([]string(nil), env...)})
		if dir == stores[0].dir {
			return beads.Bead{}, beads.ErrNotFound
		}
		if dir == stores[1].dir {
			return bead, nil
		}
		return beads.Bead{}, errors.New("unexpected work leg")
	}
	ops.ResolveWorkBranch = func(string) string { return "" }
	ops.StampWorkMeta = func(_ context.Context, dir string, env []string, _, assignee string, _ map[string]string) error {
		stampCalls.Add(1)
		stampAssignee = assignee
		stampContext = hookStore{dir: dir, env: append([]string(nil), env...)}
		return nil
	}
	ops.ListContinuation = func(_ context.Context, dir string, env []string, rootID, group string) ([]beads.Bead, error) {
		listCalls.Add(1)
		listedRoot, listedGroup = rootID, group
		listContext = hookStore{dir: dir, env: append([]string(nil), env...)}
		return []beads.Bead{sibling}, nil
	}
	ops.AssignContinuation = func(_ context.Context, dir string, env []string, beadID, assignee string) error {
		assignCalls.Add(1)
		assignedID, assignAssignee = beadID, assignee
		assignContext = hookStore{dir: dir, env: append([]string(nil), env...)}
		return nil
	}
	ops.DrainAck = func(io.Writer) error { drainCalls.Add(1); return nil }
	run := func(string, string, []string) (string, error) { runnerCalls.Add(1); return `[]`, nil }
	var stdout, stderr bytes.Buffer
	opts := currentClaimTestOpts()
	opts.Assignee = "worker-invocation"
	opts.IdentityCandidates = []string{"worker-canonical"}
	code := claimHookWorkWithRunner("query", stores[0].dir, stores[0].env, stores, opts, ops, run, func(string, error) {}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("claimHookWorkWithRunner = %d, want existing-assignment success; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if reads.Load() != 3 || len(readContexts) != 3 || readContexts[0].dir != stores[0].dir || readContexts[1].dir != stores[1].dir || readContexts[2].dir != stores[1].dir ||
		!reflect.DeepEqual(readContexts[0].env, stores[0].env) || !reflect.DeepEqual(readContexts[1].env, stores[1].env) || !reflect.DeepEqual(readContexts[2].env, stores[1].env) {
		t.Fatalf("resolver/readback reads = %d/%v, want exact rig-a then city contexts", reads.Load(), readContexts)
	}
	if resolverAssignee != opts.Assignee || stampAssignee != opts.Assignee || assignAssignee != opts.Assignee {
		t.Fatalf("actors = resolver:%q stamp:%q assign:%q, want invocation actor %q", resolverAssignee, stampAssignee, assignAssignee, opts.Assignee)
	}
	if stampCalls.Load() != 1 || stampContext.dir != stores[1].dir || !reflect.DeepEqual(stampContext.env, stores[1].env) {
		t.Fatalf("stamp = %d/%+v, want one city-leg write", stampCalls.Load(), stampContext)
	}
	if listCalls.Load() != 1 || assignCalls.Load() != 1 || listedRoot != "root-1" || listedGroup != "body" || assignedID != sibling.ID {
		t.Fatalf("continuation calls = list:%d assign:%d root/group:%q/%q id:%q, want 1/1 root-1/body/%s", listCalls.Load(), assignCalls.Load(), listedRoot, listedGroup, assignedID, sibling.ID)
	}
	if !reflect.DeepEqual(listContext.env, stores[1].env) || listContext.dir != stores[1].dir ||
		!reflect.DeepEqual(assignContext.env, stores[1].env) || assignContext.dir != stores[1].dir {
		t.Fatalf("continuation contexts = list:%+v assign:%+v, want city/%v", listContext, assignContext, stores[1].env)
	}
	if runnerCalls.Load() != 0 || drainCalls.Load() != 0 {
		t.Fatalf("runner/drain = %d/%d, want 0/0 for later-leg adoption", runnerCalls.Load(), drainCalls.Load())
	}
	var result hookClaimJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout.String())
	}
	if result.BeadID != bead.ID || result.Reason != "existing_assignment" || strings.Join(result.ContinuationAssigned, ",") != sibling.ID {
		t.Fatalf("result = %+v, want existing_assignment/%s with continuation %s", result, bead.ID, sibling.ID)
	}
}

// TestCurrentClaimCASLaterLegTransportErrorFailsClosed proves that a later-leg
// read error is not absence: no query, claim, reservation, release, drain, or
// output may follow the unresolved pointer.
func TestCurrentClaimCASLaterLegTransportErrorFailsClosed(t *testing.T) {
	store := &currentClaimCASTestStore{pointer: "unresolved"}
	stores := []hookStore{
		{dir: "rig-a", env: []string{"GC_STORE=rig-a"}},
		{dir: "city", env: []string{"GC_STORE=city"}},
	}
	var runnerCalls, claimCalls, reserveCalls, releaseCalls, drainCalls atomic.Int32
	ops := currentClaimTestOps(store, `[]`, new(atomic.Int32), new(atomic.Int32), &releaseCalls)
	ops.ReadWorkMeta = func(_ context.Context, dir string, _ []string, _, _ string) (beads.Bead, error) {
		if dir == "rig-a" {
			return beads.Bead{}, beads.ErrNotFound
		}
		return beads.Bead{}, errors.New("later work leg transport failed")
	}
	ops.Claim = func(context.Context, string, []string, string, string) (beads.Bead, bool, error) {
		claimCalls.Add(1)
		return beads.Bead{}, false, nil
	}
	ops.ReserveSessionClaim = func(sessionID, expected, next string) (beads.MetadataCASOutcome, error) {
		reserveCalls.Add(1)
		return store.reserve(sessionID, expected, next)
	}
	ops.DrainAck = func(io.Writer) error { drainCalls.Add(1); return nil }
	run := func(string, string, []string) (string, error) { runnerCalls.Add(1); return `[]`, nil }
	var stdout, stderr bytes.Buffer
	code := claimHookWorkWithRunner("query", "rig-a", stores[0].env, stores, currentClaimTestOpts(), ops, run, func(string, error) {}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("claimHookWorkWithRunner = %d, want fail-closed error", code)
	}
	if runnerCalls.Load() != 0 || claimCalls.Load() != 0 || reserveCalls.Load() != 0 || releaseCalls.Load() != 0 || drainCalls.Load() != 0 || stdout.Len() != 0 {
		t.Fatalf("downstream effects = runner:%d claim:%d reserve:%d release:%d drain:%d stdout:%q, want all zero", runnerCalls.Load(), claimCalls.Load(), reserveCalls.Load(), releaseCalls.Load(), drainCalls.Load(), stdout.String())
	}
}

// TestCurrentClaimCASAllWorkLegsNotFoundAllowsFreshReplacement proves the
// legitimate stale path reads every leg, then reserves fresh work with the
// original pointer as the CAS expected value.
func TestCurrentClaimCASAllWorkLegsNotFoundAllowsFreshReplacement(t *testing.T) {
	store := &currentClaimCASTestStore{pointer: "old-pointer"}
	stores := []hookStore{
		{dir: "rig-a", env: []string{"GC_STORE=rig-a"}},
		{dir: "city", env: []string{"GC_STORE=city"}},
	}
	row := `[{"id":"fresh","status":"open","metadata":{"gc.routed_to":"worker"}}]`
	var readDirs []string
	var expected string
	var queryCalls, claimCalls, reserveCalls atomic.Int32
	ops := currentClaimTestOps(store, row, &claimCalls, new(atomic.Int32), new(atomic.Int32))
	ops.ReadWorkMeta = func(_ context.Context, dir string, env []string, _, _ string) (beads.Bead, error) {
		readDirs = append(readDirs, dir+":"+strings.Join(env, ","))
		return beads.Bead{}, beads.ErrNotFound
	}
	ops.ResolveWorkBranch = func(string) string { return "" }
	ops.ReserveSessionClaim = func(sessionID, old, next string) (beads.MetadataCASOutcome, error) {
		reserveCalls.Add(1)
		expected = old
		return store.reserve(sessionID, old, next)
	}
	run := func(string, string, []string) (string, error) { queryCalls.Add(1); return row, nil }
	var stdout, stderr bytes.Buffer
	code := claimHookWorkWithRunner("query", "rig-a", stores[0].env, stores, currentClaimTestOpts(), ops, run, func(string, error) {}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("claimHookWorkWithRunner = %d, want fresh-claim success; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !reflect.DeepEqual(readDirs, []string{"rig-a:GC_STORE=rig-a", "city:GC_STORE=city", "rig-a:GC_STORE=rig-a"}) {
		t.Fatalf("read order/contexts = %v, want both exact pointer legs then the selected leg's canonical read", readDirs)
	}
	if queryCalls.Load() < 1 || reserveCalls.Load() != 1 || expected != "old-pointer" {
		t.Fatalf("query/claim/reserve/expected = %d/%d/%d/%q stdout=%q stderr=%q, want query>=1 claim=1 reserve=1/old-pointer", queryCalls.Load(), claimCalls.Load(), reserveCalls.Load(), expected, stdout.String(), stderr.String())
	}
}

// TestCurrentClaimCASClassResidentUsesBindingAfterRawWorkMiss proves a class
// resident pointer is resolved after every work leg misses, and downstream
// work-ledger operations use the class route without a bd work write/query.
func TestCurrentClaimCASClassResidentUsesBindingAfterRawWorkMiss(t *testing.T) {
	class := newClaimRouteClassStore(t)
	bead := mintClaimRouteBead(t, class, "gcg-current", map[string]string{
		beadmeta.SessionIDMetadataKey: "s-1", beadmeta.SessionNameMetadataKey: "worker-1", beadmeta.ClaimedAtMetadataKey: "2026-08-24T00:00:00Z",
	})
	inProgress := "in_progress"
	if err := class.Update(bead.ID, beads.UpdateOpts{Status: &inProgress, Assignee: stringPtr("worker-1")}); err != nil {
		t.Fatalf("updating class pointer bead: %v", err)
	}
	route := newFanoutClassRoute(t, class)
	stores := []hookStore{{dir: "rig-a", env: []string{"GC_STORE=rig-a"}}, {dir: "city", env: []string{"GC_STORE=city"}}}
	store := &currentClaimCASTestStore{pointer: bead.ID}
	var workReads, workWrites, runnerCalls, drainCalls atomic.Int32
	base := currentClaimTestOps(store, `[]`, new(atomic.Int32), new(atomic.Int32), new(atomic.Int32))
	base.ReadWorkMeta = func(_ context.Context, dir string, env []string, _, _ string) (beads.Bead, error) {
		workReads.Add(1)
		if !((dir == "rig-a" && reflect.DeepEqual(env, stores[0].env)) || (dir == "city" && reflect.DeepEqual(env, stores[1].env))) {
			t.Errorf("raw work read context = %s/%v, want one of exact work legs", dir, env)
		}
		return beads.Bead{}, beads.ErrNotFound
	}
	base.StampWorkMeta = func(context.Context, string, []string, string, string, map[string]string) error {
		workWrites.Add(1)
		return nil
	}
	base.ResolveWorkBranch = func(string) string { return "" }
	base.DrainAck = func(io.Writer) error { drainCalls.Add(1); return nil }
	ops := classRoutedHookClaimOps(base, route)
	var stdout, stderr bytes.Buffer
	code := claimHookWorkWithRunner("query", "rig-a", stores[0].env, stores, currentClaimTestOpts(), ops,
		func(string, string, []string) (string, error) { runnerCalls.Add(1); return `[]`, nil }, func(string, error) {}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("claimHookWorkWithRunner = %d, want class-resident adoption; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if workReads.Load() != 2 || workWrites.Load() != 0 || runnerCalls.Load() != 0 || drainCalls.Load() != 0 {
		t.Fatalf("work effects = reads:%d writes:%d runner:%d drain:%d, want 2/0/0/0", workReads.Load(), workWrites.Load(), runnerCalls.Load(), drainCalls.Load())
	}
	var result hookClaimJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout.String())
	}
	if result.BeadID != bead.ID || result.Reason != "existing_assignment" {
		t.Fatalf("result = %+v, want existing_assignment/%s", result, bead.ID)
	}
}

// TestCurrentClaimCASTopologyKeepsFullLegsAfterQueryScoping proves the
// production split between discovery and ownership: the optimized query set
// may collapse to one federated reader, but pointer residence still receives
// every ordered work leg and can adopt a later-leg claim before that reader
// runs.
func TestCurrentClaimCASTopologyKeepsFullLegsAfterQueryScoping(t *testing.T) {
	workLegs := []hookStore{
		{dir: "rig-a", env: []string{"GC_STORE=rig-a"}},
		{dir: "city", env: []string{"GC_STORE=city"}},
	}
	queryStores := scopeFederatedHookStores(workLegs, "gc ready --json", "bd query --json")
	if len(queryStores) != 1 || !sameHookStore(queryStores[0], workLegs[0]) {
		t.Fatalf("query stores = %v, want only the primary optimized leg from %v", queryStores, workLegs)
	}

	store := &currentClaimCASTestStore{pointer: "later-leg"}
	bead := beads.Bead{ID: "later-leg", Status: "in_progress", Assignee: "worker-1", Metadata: map[string]string{
		beadmeta.SessionIDMetadataKey: "s-1",
	}}
	var reads, runnerCalls, drainCalls atomic.Int32
	ops := currentClaimTestOps(store, `[]`, new(atomic.Int32), new(atomic.Int32), new(atomic.Int32))
	ops.WorkLegs = workLegs
	ops.ReadWorkMeta = func(_ context.Context, dir string, env []string, id, _ string) (beads.Bead, error) {
		reads.Add(1)
		if dir == workLegs[0].dir && reflect.DeepEqual(env, workLegs[0].env) {
			return beads.Bead{}, beads.ErrNotFound
		}
		if dir == workLegs[1].dir && reflect.DeepEqual(env, workLegs[1].env) {
			return bead, nil
		}
		return beads.Bead{}, errors.New("unexpected work leg context")
	}
	ops.ResolveWorkBranch = func(string) string { return "" }
	ops.DrainAck = func(io.Writer) error { drainCalls.Add(1); return nil }
	run := func(string, string, []string) (string, error) { runnerCalls.Add(1); return `[]`, nil }
	var stdout, stderr bytes.Buffer
	code := claimHookWorkWithRunner("query", workLegs[0].dir, workLegs[0].env, queryStores, currentClaimTestOpts(), ops, run, func(string, error) {}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("claimHookWorkWithRunner = %d, want later-leg adoption; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if reads.Load() != 3 || runnerCalls.Load() != 0 || drainCalls.Load() != 0 {
		t.Fatalf("reads/runner/drain = %d/%d/%d, want 3/0/0", reads.Load(), runnerCalls.Load(), drainCalls.Load())
	}
	var result hookClaimJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout.String())
	}
	if result.BeadID != bead.ID || result.Reason != "existing_assignment" {
		t.Fatalf("result = %+v, want existing_assignment/%s", result, bead.ID)
	}
}
