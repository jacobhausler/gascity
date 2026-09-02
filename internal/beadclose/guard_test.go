package beadclose

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

func TestMechanicalCloseAllowedRefusesBlockedWorkOutcome(t *testing.T) {
	bead := beads.Bead{
		ID:   "cr-h2e3d",
		Type: "task",
		Metadata: map[string]string{
			beadmeta.WorkOutcomeMetadataKey:      beadmeta.WorkOutcomeBlocked,
			beadmeta.WorkVerificationMetadataKey: "BLOCKED: acceptance unmet",
		},
	}
	if MechanicalCloseAllowed(bead) {
		t.Fatalf("MechanicalCloseAllowed(%+v) = true, want false for a blocked work outcome", bead)
	}
}

func TestMechanicalCloseAllowedRefusesAbandonedWorkOutcome(t *testing.T) {
	bead := beads.Bead{
		ID:   "cr-abandoned",
		Type: "task",
		Metadata: map[string]string{
			beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeAbandoned,
		},
	}
	if MechanicalCloseAllowed(bead) {
		t.Fatalf("MechanicalCloseAllowed(%+v) = true, want false for an abandoned work outcome", bead)
	}
}

func TestMechanicalCloseAllowedRefusesFailedVerificationEvenWithoutOutcome(t *testing.T) {
	bead := beads.Bead{
		ID:   "cr-failed-verify",
		Type: "task",
		Metadata: map[string]string{
			beadmeta.WorkVerificationMetadataKey: "FAIL: gate did not pass",
		},
	}
	if MechanicalCloseAllowed(bead) {
		t.Fatalf("MechanicalCloseAllowed(%+v) = true, want false for a failed verification", bead)
	}
}

func TestMechanicalCloseAllowedPermitsShippedPassingWork(t *testing.T) {
	bead := beads.Bead{
		ID:   "cr-shipped",
		Type: "task",
		Metadata: map[string]string{
			beadmeta.WorkOutcomeMetadataKey:      beadmeta.WorkOutcomeShipped,
			beadmeta.WorkVerificationMetadataKey: "PASS: gate green",
		},
	}
	if !MechanicalCloseAllowed(bead) {
		t.Fatalf("MechanicalCloseAllowed(%+v) = false, want true for a shipped/passing bead", bead)
	}
}

func TestMechanicalCloseAllowedPermitsBeadWithNoTypedWorkRecord(t *testing.T) {
	bead := beads.Bead{ID: "convoy-1", Type: "convoy"}
	if !MechanicalCloseAllowed(bead) {
		t.Fatalf("MechanicalCloseAllowed(%+v) = false, want true for a bead with no typed work record", bead)
	}
}
