package main

import (
	"testing"

	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// T2 (cr-4phwb1). A pool seat that gc itself put to sleep because there was
// nothing to wake for must release its slot.
//
// Measured live 2026-09-12 on the worker-local nomad lane: five seats sat
// state=asleep sleep_reason=no-wake-reason while cr-9papeg — P0, routed to that
// lane, unassigned, zero deps, returned by the lane's OWN ready query — went
// unclaimed for 12+ minutes. The seats were asleep AND holding their slots,
// because no-wake-reason is not in the freeable allow-list, so the pool had no
// room to start a seat that could take the work. A lane that is asleep on top of
// its own queue is the dark-lane shape exactly.
//
// gc already classifies this reason as a DELIBERATE sleep:
// session.IsDeliberateSleepReason lists SleepReasonNoWakeReason beside
// SleepReasonIdle, SleepReasonIdleTimeout and SleepReasonDrained. The freeable
// list is otherwise a near-subset of that one; this reason was simply missing
// from it. "Nothing to wake for" is idle by another name.
func TestNoWakeReasonSeatFreesItsPoolSlot(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   bool
		why    string
	}{
		{string(sessionpkg.SleepReasonNoWakeReason), true,
			"gc slept it because there was no work to wake for; that is an idle seat and its slot must recycle"},
		{string(sessionpkg.SleepReasonIdle), true, "unchanged: idle already freed its slot"},
		{string(sessionpkg.SleepReasonIdleTimeout), true, "unchanged"},
		{string(sessionpkg.SleepReasonDrained), true, "unchanged: a drained seat is freeable"},
		{"", false,
			"deny-by-default survives: an asleep seat with NO reason may be a legacy bead or a write race"},
		{"some-reason-nobody-has-defined", false,
			"deny-by-default survives for genuinely unknown reasons"},
		{string(sessionpkg.SleepReasonUserHold), false,
			"a user hold is deliberate but NOT disposable — someone parked this seat on purpose"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			info := sessionpkg.Info{MetadataState: "asleep", SleepReason: tc.reason}
			if got := isPoolSessionSlotFreeableInfo(info); got != tc.want {
				t.Fatalf("isPoolSessionSlotFreeableInfo(reason=%q) = %v, want %v — %s", tc.reason, got, tc.want, tc.why)
			}
		})
	}
}
