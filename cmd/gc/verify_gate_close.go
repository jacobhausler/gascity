package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/convoy"
)

// Verification close-gate. A formula convoy that carries an adversarial
// verification step (mol-verified-work's luna-verify is the first consumer)
// marks that step with gc.close_gate=true in its formula metadata. While an
// open workflow root owns an open close-gate step that tracks a bead, the
// `gc bd` close seam refuses to close that bead from any other caller. This
// turns the recurring "target self-closes before its verification gate ever
// ran" defect (a worker closing its own target through the CLI in direct
// violation of the formula's prose instruction) into a machine-enforced
// violation at the only documented close seam.
//
// # Why the marker, and why here
//
// The instruction "the target bead stays open for the verifier" was LLM prose
// only: nothing in the machinery stopped `gc bd update <target>
// --status=closed`. The gate is marker-based and formula-agnostic — any
// formula can opt a step in by stamping gc.close_gate=true on that step's
// metadata — so mol-verified-work is the first consumer, not the only one.
// Formulas without a close-gate step see zero behavior change: the gate only
// fires when an open workflow root owns an open step carrying the marker.
//
// # Gate logic
//
// For each bead a close-intent invocation (`bd close` or
// `bd update --status=closed`, detected by workRecordCloseTargets) would
// close:
//
//  1. DepList(target, "up") and keep the "tracks" edges. A convoy's
//     membership is Dep{IssueID: convoy, DependsOnID: target, Type:
//     "tracks"} (convoy.TrackItemIn), so the convoy IDs are the IssueIDs.
//  2. For each convoy, find the open workflow roots that consumed it
//     (gc.kind=workflow, gc.input_convoy_id=convoy). The default
//     closed-excluding query is exactly right here: a closed root means the
//     workflow finished and owes no further gate.
//  3. For each root, find the close-gate steps (gc.root_bead_id=root,
//     gc.close_gate=true) INCLUDING closed ones — a closed gate step is the
//     "the gate ran" proof, and without IncludeClosed the gate would block
//     the close forever after the gate itself completed.
//  4. If any such step is still open and the caller is not that step, the
//     close is a violation.
//
// # Caller identity
//
// The caller is resolved through the same documented chain `gc hook current`
// uses: $GC_BEAD_ID, then $GC_TRIGGER_BEAD_ID, then $GC_SESSION_ID plus the
// session claim front door (the bead the session most recently claimed
// through `gc hook --claim`). The gate step's own session closes the target
// from inside the gate (the formula's PASS close form), so caller == gate
// step is allowed. When the gate state is known (an open gate step exists)
// but the caller cannot be resolved at all, the gate FAILS CLOSED: a stalled
// close is recoverable (re-run from the gate step's session), an unverified
// close is not.
//
// # Failure modes
//
// Store read failure (DepList/ListByMetadata error) FAILS OPEN — a close is
// never blocked on the gate's own read failure, the same rule as the
// work-record gate. Gate state known but caller unresolvable FAILS CLOSED,
// as above. The two are deliberately different.
//
// # Known limits (documented, not fixed here)
//
// The raw bd binary (/opt/homebrew/bin/bd) is a separate binary and bypasses
// this preflight entirely; the city contract mandates `gc bd` for all
// mutations. A city that relocates the convoy class out of the work store
// will not see its tracks edges from the work-scope store — the same
// architectural limit as the work-record gate's fall-through path.
//
// # Enforcement
//
// ENFORCE by default: a warn-only default would not stop the defect. The
// kill-switch GC_VERIFY_GATE_ENFORCE has INVERTED semantics versus the
// work-record gate (GC_WORK_RECORD_ENFORCE): unset/empty means enforce, and
// an explicit off token (0/false/no/off) downgrades to warn-only. That is
// safe because the gate is fully opt-in per formula via the marker —
// downgrading never un-gates a formula that did not ask to be gated.

// verifyGateEnforceEnvVar downgrades the close-gate from enforce (default) to
// warn-only. Inverted versus GC_WORK_RECORD_ENFORCE: absence enforces.
const verifyGateEnforceEnvVar = "GC_VERIFY_GATE_ENFORCE"

// verifyGateMarkerValue is the gc.close_gate value the gate looks for. The
// formula stamps "true"; any other value simply does not match, which fails
// in the safe direction (no gate, no false block).
const verifyGateMarkerValue = "true"

// verifyGateQueryLimit bounds each ListByMetadata probe. A workflow root
// tracks a handful of beads and owns a handful of steps; 20 is far above any
// real formula and keeps a pathological store from making the preflight slow.
const verifyGateQueryLimit = 20

// verifyGateEnforceEnabled reports whether violations block the close
// (default) or are logged only.
func verifyGateEnforceEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(verifyGateEnforceEnvVar))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// verifyGateCloseViolation checks one close target against the close-gate and
// returns a human-readable violation ("" ⇒ the close is allowed). It is the
// store-driven core, split from the IO wrapper so it is unit-testable with an
// in-memory store. callerID is the bead identity of the session performing
// the close ("" when unresolvable); it is compared against the open gate
// steps so the gate's own session may close the target from inside the gate.
//
// Failure asymmetry: any store read error returns "" (fail open — never block
// a close on our own read failure), while a pending gate with an unresolvable
// caller returns a violation (fail closed — the stall is recoverable, the
// unverified close is not).
func verifyGateCloseViolation(store beads.Store, targetID, callerID string) string {
	deps, err := store.DepList(targetID, "up")
	if err != nil {
		return ""
	}
	for _, d := range deps {
		if d.Type != convoy.TrackingDepType {
			continue
		}
		convoyID := d.IssueID
		roots, err := store.ListByMetadata(map[string]string{
			beadmeta.KindMetadataKey:          beadmeta.KindWorkflow,
			beadmeta.InputConvoyIDMetadataKey: convoyID,
		}, verifyGateQueryLimit)
		if err != nil {
			return ""
		}
		for _, root := range roots {
			steps, err := store.ListByMetadata(map[string]string{
				beadmeta.RootBeadIDMetadataKey: root.ID,
				beadmeta.CloseGateMetadataKey:  verifyGateMarkerValue,
			}, verifyGateQueryLimit, beads.IncludeClosed)
			if err != nil {
				return ""
			}
			for _, step := range steps {
				if step.Status == "closed" {
					continue // the gate ran; its completion is the proof
				}
				if step.ID == callerID {
					continue // the gate's own session closing from inside the gate
				}
				return fmt.Sprintf(
					"refusing to close %s: open verification gate step %s (workflow %s) has not run to completion; only the gate step's own session may close tracked work",
					targetID, step.ID, root.ID)
			}
		}
	}
	return ""
}

// verifyGateSessionClaim resolves the bead a session most recently claimed
// through `gc hook --claim`, through the routed session-class front door (so
// a [beads.classes.sessions] relocation is honored, exactly as `gc hook
// current` does). It is a var so tests can substitute a stub.
var verifyGateSessionClaim = func(sessionID string) (string, error) {
	sessFront, err := sessionCurrentClaimFrontDoor()
	if err != nil {
		return "", err
	}
	return sessFront.CurrentClaimBeadID(sessionID)
}

// resolveVerifyGateCallerID names the bead identity of the session performing
// the close, through the documented `gc hook current` chain: $GC_BEAD_ID,
// then $GC_TRIGGER_BEAD_ID, then $GC_SESSION_ID plus the session claim front
// door. Returns "" when nothing resolves — the caller then fails closed
// against any pending gate.
func resolveVerifyGateCallerID() string {
	if id := strings.TrimSpace(os.Getenv("GC_BEAD_ID")); id != "" {
		return id
	}
	if id := strings.TrimSpace(os.Getenv("GC_TRIGGER_BEAD_ID")); id != "" {
		return id
	}
	sessionID := strings.TrimSpace(os.Getenv("GC_SESSION_ID"))
	if sessionID == "" {
		return ""
	}
	id, err := verifyGateSessionClaim(sessionID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(id)
}

// runVerifyGateCloseGuard is the IO wrapper the bd fall-through runs after the
// work-record close gate. It reuses the store the write-ID guard already
// opened (nil ⇒ fail open, as in the work-record gate) and returns whether the
// close should be blocked (only in enforce mode, the default).
func runVerifyGateCloseGuard(bdArgs []string, store beads.Store, stderr io.Writer) bool {
	targets, ok := workRecordCloseTargets(bdArgs)
	if !ok {
		return false
	}
	if store == nil {
		// Cannot verify — never block a close on our own read failure.
		return false
	}
	callerID := resolveVerifyGateCallerID()
	enforce := verifyGateEnforceEnabled()
	blocked := false
	for _, id := range targets {
		violation := verifyGateCloseViolation(store, id, callerID)
		if violation == "" {
			continue
		}
		if enforce {
			fmt.Fprintf(stderr, "gc bd: close-gate: %s\n", violation) //nolint:errcheck // best-effort stderr
			blocked = true
		} else {
			fmt.Fprintf(stderr, "gc bd: close-gate (warn-only; unset %s to enforce): %s\n", verifyGateEnforceEnvVar, violation) //nolint:errcheck // best-effort stderr
		}
	}
	return blocked
}
