package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A finished one_shot seat inside a runtime box must be able to say "I am done"
// without any possibility of claiming on the way out.
//
// Before the claim-free ack existed there was no such door. `gc runtime
// drain-ack` is local-only (currentSessionRuntimeTarget needs a resolvable city
// path, which the remote contract forbids and the nomad pack strips), and
// `--claim --drain-ack` acknowledges the drain only on a NO-WORK result — so a
// finished seat calling it picks up another bead whenever the pool has work and
// exits holding it. The claim path is lifecycle-blind: nothing in
// cmd_hook_claim.go or internal/config/workquery.go consults one_shot.
//
// Measured consequence on a live lane, 2026-09-11: the seat holds its pool slot
// forever, the lane serves max_active_sessions atoms and then goes dark while
// every observer reports healthy running seats.
func TestHookDrainAckWithoutClaimIsRefusedLocallyAndNamesTheLocalSpelling(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// No remote target configured, so this resolves as a local city.
	code := cmdHookWithOptions(nil, hookCommandOptions{DrainAck: true}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("local --drain-ack without --claim must not succeed: the local claim-free ack is `gc runtime drain-ack`")
	}
	msg := stderr.String()
	if !strings.Contains(msg, "gc runtime drain-ack") {
		t.Fatalf("the local refusal must NAME the better spelling rather than restating the flag rule; got: %q", msg)
	}
	// The old message was "--drain-ack requires --claim", which sent the caller
	// to the one spelling that can claim a bead on its way out.
	if strings.Contains(msg, "requires --claim") {
		t.Fatalf("the refusal must not send a finishing seat to --claim; got: %q", msg)
	}
}

// The claim-free ack must never enter the claim protocol. This pins the
// property by construction: remoteHookDrainAck's body may not reference the
// claim entry points, so it cannot come back holding work.
func TestRemoteDrainAckNeverEntersTheClaimProtocol(t *testing.T) {
	src := readSourceFile(t, "remote_worker.go")
	start := strings.Index(src, "func remoteHookDrainAck(")
	if start < 0 {
		t.Fatal("remoteHookDrainAck is missing: there is no claim-free remote ack")
	}
	// End at the function's own closing brace, NOT at the next "\nfunc ": that
	// would swallow the following function's DOC COMMENT, and the next function
	// here is remoteHookClaim — so a bare scan indicts this function for a name
	// that appears only in its neighbour's prose. Same name-is-not-an-invocation
	// trap this codebase has hit repeatedly; a test that greps source has to
	// bound its slice to code it actually owns.
	end := strings.Index(src[start:], "\n}\n")
	if end < 0 {
		t.Fatal("remoteHookDrainAck has no closing brace at column 0")
	}
	body := src[start : start+end]

	if !strings.Contains(body, "WorkerDrainAck") {
		t.Fatal("remoteHookDrainAck must post the drain acknowledgement")
	}
	for _, forbidden := range []string{"remoteHookClaim", "WorkerClaim", "workQuery", "WorkerCurrent"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("remoteHookDrainAck references %q — a seat on its way out must not be able to claim or read the work query", forbidden)
		}
	}
}

// readSourceFile reads a file from this package's own directory so a test can
// pin a structural property of the source rather than only its behaviour.
func readSourceFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(".", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
