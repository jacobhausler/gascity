package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// stampMetaSpy captures the (beadID, assignee, patch) a claim writes through the
// StampWorkMeta seam, and lets a test inject a write error to prove the stamp
// never fails the claim. The patch is copied so the assertion is stable.
type stampMetaSpy struct {
	calls    int
	beadID   string
	assignee string
	patch    map[string]string
	err      error
}

func (s *stampMetaSpy) fn(_ context.Context, _ string, _ []string, beadID, assignee string, patch map[string]string) error {
	s.calls++
	s.beadID, s.assignee = beadID, assignee
	s.patch = map[string]string{}
	for k, v := range patch {
		s.patch[k] = v
	}
	return s.err
}

// noopPublishRunMap keeps claim tests focused on work-bead identity stamping.
func noopPublishRunMap(string, string, ...string) error {
	return nil
}

// sessionClaimClearSpy captures the expected-value CAS clear used by an
// undelivered minted claim. It cannot issue an unconditional empty write that
// might erase a later winner.
type sessionClaimClearSpy struct {
	calls     int
	sessionID string
	expected  []string
	outcome   beads.MetadataCASOutcome
	err       error
}

func (s *sessionClaimClearSpy) fn(sessionID, expected string) (beads.MetadataCASOutcome, error) {
	s.calls++
	s.sessionID = sessionID
	s.expected = append(s.expected, expected)
	if s.outcome == "" && s.err == nil {
		s.outcome = beads.MetadataCASSwapped
	}
	return s.outcome, s.err
}

// noopStampWorkMeta suppresses the work-bead identity stamp so claim tests that
// don't assert on it stay hermetic — the default seam issues a real bd subprocess
// write, which fires now that a claim stamps session identity whenever GC_SESSION_ID
// is set.
func noopStampWorkMeta(context.Context, string, []string, string, string, map[string]string) error {
	return nil
}

func noopReadSessionClaim(string) (string, error) { return "", nil }

func noopReserveSessionClaim(string, string, string) (beads.MetadataCASOutcome, error) {
	return beads.MetadataCASSwapped, nil
}

func noopClearSessionClaim(string, string) (beads.MetadataCASOutcome, error) {
	return beads.MetadataCASSwapped, nil
}

// popClaimedAt extracts and validates the write-once gc.claimed_at entry from
// a fresh-claim patch, returning the remaining keys so the caller can assert
// them by exact equality without hardcoding the dynamic clock value. It fails
// the test if the key is absent, unparseable, or not recent — every claim in
// this file's fresh-claim tests is expected to carry one.
func popClaimedAt(t *testing.T, patch map[string]string) map[string]string {
	t.Helper()
	raw, ok := patch[beadmeta.ClaimedAtMetadataKey]
	if !ok {
		t.Fatalf("patch = %v, want gc.claimed_at present on a fresh claim", patch)
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("gc.claimed_at = %q is not RFC3339: %v", raw, err)
	}
	if age := time.Since(ts); age < 0 || age > 10*time.Second {
		t.Fatalf("gc.claimed_at = %q is not a recent timestamp (age %s)", raw, age)
	}
	rest := make(map[string]string, len(patch)-1)
	for k, v := range patch {
		if k != beadmeta.ClaimedAtMetadataKey {
			rest[k] = v
		}
	}
	return rest
}

// poolClaimOps builds the seam for a pool slot claiming an unassigned,
// route-matched candidate: the runner yields it, Claim returns it owned by us,
// the branch resolver returns branch, and StampWorkMeta is captured by spy.
func poolClaimOps(runner string, claimedMeta map[string]string, branch string, spy *stampMetaSpy) hookClaimOps {
	return hookClaimOps{
		Runner: func(string, string) (string, error) { return runner, nil },
		Claim: func(_ context.Context, _ string, _ []string, id, assignee string) (beads.Bead, bool, error) {
			return beads.Bead{ID: id, Status: "in_progress", Assignee: assignee, Metadata: claimedMeta}, true, nil
		},
		ResolveWorkBranch: func(string) string { return branch },
		StampWorkMeta:     spy.fn,
		ReadWorkMeta: func(_ context.Context, _ string, _ []string, id, assignee string) (beads.Bead, error) {
			meta := map[string]string{}
			for k, v := range claimedMeta {
				meta[k] = v
			}
			meta[beadmeta.SessionIDMetadataKey] = "mc-sess1"
			meta[beadmeta.SessionNameMetadataKey] = "gc__role-mc-sess1"
			return beads.Bead{ID: id, Status: "in_progress", Assignee: assignee, Metadata: meta}, nil
		},
		PublishRunMap:       noopPublishRunMap,
		ReadSessionClaim:    noopReadSessionClaim,
		ReserveSessionClaim: noopReserveSessionClaim,
		ClearSessionClaim:   noopClearSessionClaim,
	}
}

// poolClaimOpts is a pool slot's claim options: assignee is the pool session name
// and the env carries both GC_SESSION_ID (the session bead id) and
// GC_SESSION_NAME (the pool session name).
func poolClaimOpts() hookClaimOptions {
	return hookClaimOptions{
		Assignee:           "gc__role-mc-sess1",
		IdentityCandidates: []string{"gc__role-mc-sess1"},
		RouteTargets:       []string{"worker"},
		Env:                []string{"GC_SESSION_ID=mc-sess1", "GC_SESSION_NAME=gc__role-mc-sess1"},
		JSON:               true,
	}
}

// TestDoHookClaimStampsSessionIdentity is the primary claim-time back-reference
// test: a fresh pool claim stamps gc.session_id + gc.session_name onto the work
// bead alongside gc.work_branch, in ONE patch. Fails before the fix, which stamped
// only the branch.
func TestDoHookClaimStampsSessionIdentity(t *testing.T) {
	spy := &stampMetaSpy{}
	ops := poolClaimOps(
		`[{"id":"hw-pool","status":"open","metadata":{"gc.routed_to":"worker"}}]`,
		map[string]string{"gc.routed_to": "worker"},
		"bd-hw-pool",
		spy,
	)

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}
	if spy.calls != 1 {
		t.Fatalf("StampWorkMeta calls = %d, want 1", spy.calls)
	}
	want := map[string]string{
		beadmeta.WorkBranchMetadataKey:  "bd-hw-pool",
		beadmeta.SessionIDMetadataKey:   "mc-sess1",
		beadmeta.SessionNameMetadataKey: "gc__role-mc-sess1",
	}
	rest := popClaimedAt(t, spy.patch)
	if !reflect.DeepEqual(rest, want) {
		t.Fatalf("patch (minus gc.claimed_at) = %v, want %v", rest, want)
	}
	if spy.beadID != "hw-pool" || spy.assignee != "gc__role-mc-sess1" {
		t.Fatalf("stamp target = bead %q assignee %q, want hw-pool / gc__role-mc-sess1", spy.beadID, spy.assignee)
	}
}

// TestDoHookClaimStampsSessionIdentityOnAdoption covers the adoption path: a bead
// already in_progress and owned by this session (existing_assignment, no fresh
// Claim) still receives the session back-reference, since the stamp re-runs on
// every hook tick that adopts the bead.
func TestDoHookClaimStampsSessionIdentityOnAdoption(t *testing.T) {
	spy := &stampMetaSpy{}
	ops := hookClaimOps{
		Runner: func(string, string) (string, error) {
			return `[{"id":"hw-adopt","status":"in_progress","assignee":"gc__role-mc-sess1","metadata":{"gc.routed_to":"worker"}}]`, nil
		},
		Claim: func(context.Context, string, []string, string, string) (beads.Bead, bool, error) {
			t.Error("Claim must not be called on the existing-assignment path")
			return beads.Bead{}, false, nil
		},
		ResolveWorkBranch:   func(string) string { return "" }, // no worktree
		StampWorkMeta:       spy.fn,
		PublishRunMap:       noopPublishRunMap,
		ReadSessionClaim:    noopReadSessionClaim,
		ReserveSessionClaim: noopReserveSessionClaim,
		ClearSessionClaim:   noopClearSessionClaim,
	}

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}
	want := map[string]string{
		beadmeta.SessionIDMetadataKey:   "mc-sess1",
		beadmeta.SessionNameMetadataKey: "gc__role-mc-sess1",
	}
	if spy.calls != 1 {
		t.Fatalf("stamp calls = %d, want 1", spy.calls)
	}
	rest := popClaimedAt(t, spy.patch)
	if !reflect.DeepEqual(rest, want) {
		t.Fatalf("patch (minus gc.claimed_at) = %v, want %v", rest, want)
	}
}

// TestDoHookClaimStampsSessionIdentityWithoutWorktree pins sessionVerify #1: when
// the worktree resolves no branch (no repo / detached HEAD), the session
// back-reference is STILL stamped — it must not be buried behind the branch
// early-return.
func TestDoHookClaimStampsSessionIdentityWithoutWorktree(t *testing.T) {
	spy := &stampMetaSpy{}
	ops := poolClaimOps(
		`[{"id":"hw-nobranch","status":"open","metadata":{"gc.routed_to":"worker"}}]`,
		map[string]string{"gc.routed_to": "worker"},
		"", // no branch
		spy,
	)

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}
	want := map[string]string{
		beadmeta.SessionIDMetadataKey:   "mc-sess1",
		beadmeta.SessionNameMetadataKey: "gc__role-mc-sess1",
	}
	if spy.calls != 1 {
		t.Fatalf("stamp calls = %d, want 1", spy.calls)
	}
	rest := popClaimedAt(t, spy.patch)
	if !reflect.DeepEqual(rest, want) {
		t.Fatalf("patch (minus gc.claimed_at) = %v, want %v (session id/name even with no worktree)", rest, want)
	}
}

// TestDoHookClaimSkipsStampWhenIdentityUnchanged pins the mandatory idempotence
// guard (sessionVerify #2): a candidate already carrying the current branch,
// session identity, AND a prior gc.claimed_at produces NO write, so the per-tick
// adoption re-run does not flood bead.updated events. gc.claimed_at must be
// preset here too (write-once): without it, this "everything unchanged"
// candidate would still get a fresh claimed_at added to its patch, falsely
// failing the "no write" assertion.
func TestDoHookClaimSkipsStampWhenIdentityUnchanged(t *testing.T) {
	spy := &stampMetaSpy{}
	current := map[string]string{
		"gc.routed_to":    "worker",
		"gc.work_branch":  "bd-hw-idem",
		"gc.session_id":   "mc-sess1",
		"gc.session_name": "gc__role-mc-sess1",
		"gc.claimed_at":   "2026-01-01T00:00:00Z",
	}
	ops := poolClaimOps(
		`[{"id":"hw-idem","status":"open","metadata":{"gc.routed_to":"worker","gc.work_branch":"bd-hw-idem","gc.session_id":"mc-sess1","gc.session_name":"gc__role-mc-sess1","gc.claimed_at":"2026-01-01T00:00:00Z"}}]`,
		current,
		"bd-hw-idem",
		spy,
	)

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}
	if spy.calls != 0 {
		t.Fatalf("StampWorkMeta calls = %d, want 0 (branch + session identity already current)", spy.calls)
	}
}

// TestDoHookClaimStampsOnlyChangedIdentityKeys proves the patch is minimal: a
// candidate whose session identity and gc.claimed_at are current but whose branch
// changed writes ONLY the branch, leaving the unchanged keys (including
// gc.claimed_at, preset here since it is write-once) out of the patch.
func TestDoHookClaimStampsOnlyChangedIdentityKeys(t *testing.T) {
	spy := &stampMetaSpy{}
	current := map[string]string{
		"gc.routed_to":    "worker",
		"gc.work_branch":  "bd-old",
		"gc.session_id":   "mc-sess1",
		"gc.session_name": "gc__role-mc-sess1",
		"gc.claimed_at":   "2026-01-01T00:00:00Z",
	}
	ops := poolClaimOps(
		`[{"id":"hw-partial","status":"open","metadata":{"gc.routed_to":"worker","gc.work_branch":"bd-old","gc.session_id":"mc-sess1","gc.session_name":"gc__role-mc-sess1","gc.claimed_at":"2026-01-01T00:00:00Z"}}]`,
		current,
		"bd-new",
		spy,
	)

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}
	want := map[string]string{beadmeta.WorkBranchMetadataKey: "bd-new"}
	if spy.calls != 1 || !reflect.DeepEqual(spy.patch, want) {
		t.Fatalf("stamp = {calls:%d patch:%v}, want {1 %v} (only the changed key)", spy.calls, spy.patch, want)
	}
}

// TestDoHookClaimSkipsSessionIdentityForControlBead pins the control-bead edge
// policy (sessionVerify #3): a control-dispatcher session claiming a control bead
// (gc.kind in ControlKinds) must NOT acquire a session back-reference — control
// steps stay session-free by graphroute's design — while gc.work_branch is still
// stamped as before. gc.claimed_at, unlike the session keys, is UNCONDITIONAL: a
// claim timestamp is meaningful for every claimed bead regardless of kind, so a
// control bead's first claim still stamps it (OBS-001; see also
// TestHookClaimIdentityPatchClaimedAtForControlBead for the direct-patch unit
// test pinning this same decision).
func TestDoHookClaimSkipsSessionIdentityForControlBead(t *testing.T) {
	spy := &stampMetaSpy{}
	ops := poolClaimOps(
		`[{"id":"hc-check","status":"open","metadata":{"gc.routed_to":"worker","gc.kind":"check"}}]`,
		map[string]string{"gc.routed_to": "worker", "gc.kind": "check"},
		"bd-hc-check",
		spy,
	)

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}
	want := map[string]string{beadmeta.WorkBranchMetadataKey: "bd-hc-check"}
	if spy.calls != 1 {
		t.Fatalf("stamp calls = %d, want 1", spy.calls)
	}
	rest := popClaimedAt(t, spy.patch)
	if !reflect.DeepEqual(rest, want) {
		t.Fatalf("patch (minus gc.claimed_at) = %v, want %v (no session keys on a control bead, but claimed_at IS stamped)", rest, want)
	}
}

// TestDoHookClaimSkipsSessionIdentityWhenNoSessionID: a non-session run (no
// GC_SESSION_ID) has no session bead to reference, so neither session key is
// stamped even when GC_SESSION_NAME happens to be set. gc.claimed_at is still
// stamped: it is not gated on session identity.
func TestDoHookClaimSkipsSessionIdentityWhenNoSessionID(t *testing.T) {
	spy := &stampMetaSpy{}
	ops := poolClaimOps(
		`[{"id":"hw-nosess","status":"open","metadata":{"gc.routed_to":"worker"}}]`,
		map[string]string{"gc.routed_to": "worker"},
		"bd-hw-nosess",
		spy,
	)
	opts := poolClaimOpts()
	opts.Env = []string{"GC_SESSION_NAME=gc__role-mc-sess1"} // GC_SESSION_ID absent

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", opts, ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}
	want := map[string]string{beadmeta.WorkBranchMetadataKey: "bd-hw-nosess"}
	if spy.calls != 1 {
		t.Fatalf("stamp calls = %d, want 1", spy.calls)
	}
	rest := popClaimedAt(t, spy.patch)
	if !reflect.DeepEqual(rest, want) {
		t.Fatalf("patch (minus gc.claimed_at) = %v, want %v (no session id ⇒ no session keys, but claimed_at IS stamped)", rest, want)
	}
}

// TestDoHookClaimIdentityStampFailureDoesNotFailClaim proves the stamp is
// best-effort: a failing StampWorkMeta logs to stderr but the claim still exits 0
// and reports the claimed bead id.
func TestDoHookClaimIdentityStampFailureDoesNotFailClaim(t *testing.T) {
	spy := &stampMetaSpy{err: errors.New("dolt boom")}
	ops := poolClaimOps(
		`[{"id":"hw-err","status":"open","metadata":{"gc.routed_to":"worker"}}]`,
		map[string]string{"gc.routed_to": "worker"},
		"bd-hw-err",
		spy,
	)

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0 (stamp error must not fail the claim); stderr=%s", code, stderr.String())
	}
	var result hookClaimJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v\nraw: %s", err, stdout.String())
	}
	if result.BeadID != "hw-err" || result.Reason != "claimed" {
		t.Fatalf("claim result = %+v, want bead hw-err reason claimed", result)
	}
}

func TestDoHookClaimEmitsStartedOnlyAfterDurableSessionReadback(t *testing.T) {
	spy := &stampMetaSpy{}
	meta := map[string]string{
		"gc.routed_to": "worker", beadmeta.RootBeadIDMetadataKey: "gcg-run",
		beadmeta.StepIDMetadataKey: "build", beadmeta.NativeStepDependenciesMetadataKey: `["prepare"]`,
	}
	ops := poolClaimOps(`[{"id":"gcg-attempt","status":"open","metadata":{"gc.routed_to":"worker"}}]`, meta, "", spy)
	var emitted []beads.Bead
	ops.EmitExecutionStepStarted = func(b beads.Bead, _ string, _ []string, _ string) { emitted = append(emitted, b) }
	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d; stderr=%s", code, stderr.String())
	}
	if len(emitted) != 1 || emitted[0].ID != "gcg-attempt" || emitted[0].Metadata[beadmeta.SessionIDMetadataKey] != "mc-sess1" || emitted[0].Status != "in_progress" {
		t.Fatalf("started emission = %#v, want one durable in-progress session-stamped step", emitted)
	}

	spy.err = errors.New("stamp failed")
	emitted = nil
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("failed-stamp claim = %d; stderr=%s", code, stderr.String())
	}
	if len(emitted) != 0 {
		t.Fatalf("failed stamp emitted started event: %#v", emitted)
	}
}

// TestDoHookClaimAdoptionReconcilesDurableStartedFact covers the crash window
// between a durable claim-time identity stamp and event recording. A later hook
// tick adopts the same in-progress assignment, verifies its durable identity,
// and must re-emit the idempotent started fact rather than leave a permanent
// lifecycle gap. The adopted bead already carries gc.claimed_at from its
// original claim (write-once), so the adoption tick must not re-stamp it.
func TestDoHookClaimAdoptionReconcilesDurableStartedFact(t *testing.T) {
	spy := &stampMetaSpy{}
	meta := map[string]string{
		"gc.routed_to": "worker", beadmeta.RootBeadIDMetadataKey: "gcg-run",
		beadmeta.StepIDMetadataKey: "build", beadmeta.SessionIDMetadataKey: "mc-sess1",
		beadmeta.SessionNameMetadataKey: "gc__role-mc-sess1",
		beadmeta.ClaimedAtMetadataKey:   "2026-01-01T00:00:00Z",
	}
	ops := hookClaimOps{
		Runner: func(string, string) (string, error) {
			return `[{"id":"gcg-attempt","status":"in_progress","assignee":"gc__role-mc-sess1","metadata":{"gc.routed_to":"worker","gc.root_bead_id":"gcg-run","gc.step_id":"build","gc.session_id":"mc-sess1","gc.session_name":"gc__role-mc-sess1","gc.claimed_at":"2026-01-01T00:00:00Z"}}]`, nil
		},
		ResolveWorkBranch: func(string) string { return "" },
		StampWorkMeta:     spy.fn,
		ReadWorkMeta: func(_ context.Context, _ string, _ []string, id, assignee string) (beads.Bead, error) {
			return beads.Bead{ID: id, Status: "in_progress", Assignee: assignee, Metadata: meta}, nil
		},
		PublishRunMap:       noopPublishRunMap,
		ReadSessionClaim:    noopReadSessionClaim,
		ReserveSessionClaim: noopReserveSessionClaim,
		ClearSessionClaim:   noopClearSessionClaim,
	}
	var emitted []beads.Bead
	ops.EmitExecutionStepStarted = func(b beads.Bead, _ string, _ []string, _ string) { emitted = append(emitted, b) }

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/work", poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d; stderr=%s", code, stderr.String())
	}
	if spy.calls != 0 {
		t.Fatalf("StampWorkMeta calls = %d, want 0 for an already durable identity", spy.calls)
	}
	if len(emitted) != 1 || emitted[0].ID != "gcg-attempt" || emitted[0].Metadata[beadmeta.SessionIDMetadataKey] != "mc-sess1" {
		t.Fatalf("started emission = %#v, want durable adopted step", emitted)
	}
}
