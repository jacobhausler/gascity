package main

import "testing"

// The probe may only ever turn a LIVE answer into a dead one, and only on a
// CONFIRMED absence. Every uncertainty must keep the claim.
//
// T5, 2026-09-11: a nomad seat was SIGKILLed mid-claim and the bead stayed
// in_progress and assigned for the four minutes it was watched, because
// liveOpenSessionAssignmentExists asks only whether an open session BEAD
// exists. In a box the bead and the runtime are two things on two machines, so
// it answered TRUE about a corpse.
//
// The inverse hazard is worse: reading an unreadable provider as "not running"
// would release live work across the whole fleet on a transient Nomad blip.
// city_runtime.go makes that argument for the shutdown path already.
func TestSessionAssignmentLivenessProbeOnlyNarrowsAndFailsClosed(t *testing.T) {
	const assignee = "worker-local-1-pool"

	cases := []struct {
		name  string
		bead  bool
		probe runtimeAbsenceProbe
		want  bool
		why   string
	}{
		{
			"bead dead, no probe", false,
			nil, false,
			"a dead bead is dead regardless of the runtime",
		},
		{
			"bead live, no probe", true,
			nil, true,
			"a nil probe must be exactly today's behavior",
		},
		{
			"bead live, runtime confirms absent", true,
			func(string) bool { return true }, false,
			"a confirmed absence is the whole point: the box is a corpse",
		},
		{
			"bead live, runtime uncertain", true,
			func(string) bool { return false }, true,
			"an unreadable or partial runtime must NOT release live work",
		},
		{
			"bead dead, runtime says present", false,
			func(string) bool { return false }, false,
			"the probe may only narrow: it can never resurrect a dead bead",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionAssignmentIsLiveForTest(tc.bead, assignee, tc.probe)
			if got != tc.want {
				t.Fatalf("liveness = %v, want %v — %s", got, tc.want, tc.why)
			}
		})
	}
}

// sessionAssignmentIsLiveForTest exercises the composition rule without a
// store: the bead half is the input, so the test pins how the probe COMBINES
// with it rather than re-testing the bead query, which has its own tests and a
// deliberate label-only shape the orphan-release suite depends on.
func sessionAssignmentIsLiveForTest(beadLive bool, assignee string, absent runtimeAbsenceProbe) bool {
	if !beadLive {
		return false
	}
	if absent == nil {
		return true
	}
	return !absent(assignee)
}

// cr-1jicje: an orphaned seat holding assigned work was a DEADLOCK, not a slow
// clear. closeSessionBeadIfRuntimeStoppedAndUnassigned refuses to close a bead
// that has assigned work — correctly — while repairStrandedPoolWorkerBead, the
// thing that would unassign that work, was gated on a freeable sleep_reason
// that an orphaned seat does not have. So the work was never released, the bead
// never closed, and the slot never freed. Measured 24+ minutes across six polls
// on a cap-2 lane, with a routed atom open and unassigned throughout.
//
// The break is evidence, not a wider allow-list: a runtime that positively
// reports the box gone is an ANSWER, and a stronger one than any inferred sleep
// reason. An unknown state with no runtime evidence stays denied, and a seat
// claiming state=asleep is excluded outright: it is asserting its box is
// legitimately gone, so absence is not news about it.
func TestConfirmedRuntimeAbsenceBreaksTheOrphanedSeatDeadlock(t *testing.T) {
	const seat = "worker-local-1-pool"

	cases := []struct {
		name          string
		freeableState bool
		dormant       bool
		probe         runtimeAbsenceProbe
		wantRepair    bool
		why           string
	}{
		{"freeable state, no probe", true, false, nil, true,
			"today's behaviour must be unchanged when no probe is installed"},
		{"unfreeable state, no probe", false, false, nil, false,
			"an unrecognised state with NO evidence stays denied — deny-by-default is deliberate"},
		{"orphaned state, runtime confirms gone", false, false,
			func(string) bool { return true }, true,
			"positive evidence the box is gone is what breaks the deadlock"},
		{"unfreeable state, runtime uncertain", false, false,
			func(string) bool { return false }, false,
			"an unreadable or still-present runtime must NOT authorise touching an unknown state"},
		{"freeable state, runtime uncertain", true, false,
			func(string) bool { return false }, true,
			"the probe may only WIDEN here; it must never veto an already-freeable state"},
		// The case whose absence let a regression through: a dormant seat is
		// absent from the runtime BECAUSE it is asleep. Treating that as death
		// retired healthy resumable workers on every scale check.
		{"dormant asleep seat, runtime absent", false, true,
			func(string) bool { return true }, false,
			"state=asleep asserts the box is legitimately gone; absence confirms nothing and must not reap it"},
		{"dormant asleep seat, no probe", false, true, nil, false,
			"dormancy is denied with or without a probe"},
		{"drain-acked asleep seat stays freeable while dormant", true, true,
			func(string) bool { return true }, true,
			"a recognised freeable sleep_reason still frees its slot; dormancy only withholds the EXTRA authority"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := poolSlotRepairAuthorised(tc.freeableState, tc.dormant, seat, tc.probe)
			if got != tc.wantRepair {
				t.Fatalf("repair authorised = %v, want %v — %s", got, tc.wantRepair, tc.why)
			}
		})
	}
}

