// Package beadclose provides a single fail-closed invariant shared by every
// mechanical/cache/convoy bead-closure path in Gas City: molecule subtree
// cleanup (internal/molecule), workflow subtree cleanup
// (internal/sourceworkflow), and the convoy/molecule root autoclose hooks
// (cmd/gc). None of those paths are the gated `gc bd close` seam
// (cmd/gc/work_record_gate.go, ADR-0009) — they close beads directly through
// the store because they are best-effort infrastructure invoked from bd hook
// scripts and reconcile loops, not a human or worker reporting a typed work
// outcome. That made it possible for a mechanical cascade (e.g. an input
// convoy autoclose reached from an unrelated bead's close) to force-close a
// bead whose own typed work record said the acceptance was never met —
// gc.work_outcome=blocked, gc.work_verification explicitly BLOCKED — with a
// generic close reason and no human review (see gastownhall/gascity
// cache-reconcile false-close incident, cr-h2e3d).
//
// MechanicalCloseAllowed is the one guard every such path must consult before
// flipping a bead's status to closed. It is permissive by default: a bead
// that carries no typed work-record metadata (structural beads, convoys,
// molecule/workflow roots, and beads a worker never claimed) is unaffected,
// preserving every existing autoclose behavior. It only refuses when the
// bead's own metadata affirmatively says the work did not land — deliberately
// narrow, so this is a structural invariant, not a judgment call: the Go code
// makes no decision about the work itself, it only refuses to contradict a
// decision the bead already recorded.
package beadclose

import (
	"strings"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// MechanicalCloseAllowed reports whether a mechanical/cache/convoy closure
// path may close bead. It returns false — refuse to close — exactly when the
// bead's own typed work-record metadata says the work is not a terminal
// success:
//
//   - gc.work_outcome is "blocked" or "abandoned" (ADR-0009's non-terminal
//     dispositions; only "shipped" and "no-op" represent completed work).
//   - gc.work_verification's text names a BLOCKED or FAILED result.
//
// A bead carrying neither key (the overwhelming majority of structural,
// convoy, and molecule/workflow-root beads) returns true: this guard adds no
// new refusal for beads that never had a typed work record to contradict.
func MechanicalCloseAllowed(bead beads.Bead) bool {
	return MechanicalCloseAllowedMetadata(bead.Metadata)
}

// MechanicalCloseAllowedMetadata is MechanicalCloseAllowed over a raw
// metadata map, for callers that have not materialized a full beads.Bead
// (e.g. a subtree walk that only fetched metadata).
func MechanicalCloseAllowedMetadata(metadata map[string]string) bool {
	outcome := strings.TrimSpace(metadata[beadmeta.WorkOutcomeMetadataKey])
	switch outcome {
	case beadmeta.WorkOutcomeBlocked, beadmeta.WorkOutcomeAbandoned:
		return false
	}

	verification := strings.ToUpper(strings.TrimSpace(metadata[beadmeta.WorkVerificationMetadataKey]))
	if verification == "" {
		return true
	}
	if strings.Contains(verification, "BLOCKED") || strings.Contains(verification, "FAIL") {
		return false
	}
	return true
}
