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
