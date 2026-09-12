package main

import (
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

func isDrainedSessionMetadata(meta map[string]string) bool {
	state := strings.TrimSpace(meta["state"])
	if state == "drained" {
		return true
	}
	return state == "asleep" && strings.TrimSpace(meta["sleep_reason"]) == string(sessionpkg.SleepReasonDrained)
}

func isDrainedSessionBead(session beads.Bead) bool {
	return isDrainedSessionMetadata(session.Metadata)
}

// isDrainedSessionInfo is the session.Info mirror of isDrainedSessionBead. It
// reads the RAW metadata state (Info.MetadataState) and sleep reason
// (Info.SleepReason), matching the bead form's untrimmed-key reads.
func isDrainedSessionInfo(i sessionpkg.Info) bool {
	state := strings.TrimSpace(i.MetadataState)
	if state == "drained" {
		return true
	}
	return state == "asleep" && strings.TrimSpace(i.SleepReason) == string(sessionpkg.SleepReasonDrained)
}

// poolSessionIsLiveInfo reports whether a pool session represents an actively
// running session for the runningSessions counter in build_desired_state. An
// asleep or drained session is not live — it holds no active process and must
// not suppress the isCold cross-store wake probe. It reads the RAW state
// metadata (Info.MetadataState) and delegates the drained/asleep-drained check
// to isDrainedSessionInfo, matching the untrimmed-key reads the bead carried.
func poolSessionIsLiveInfo(i sessionpkg.Info) bool {
	if strings.TrimSpace(i.MetadataState) == "asleep" {
		return false
	}
	if isDrainedSessionInfo(i) {
		return false
	}
	return true
}

// isPoolSessionSlotFreeable reports whether a session's bead is in a terminal
// state where the pool slot it occupies can be freed: explicitly drained, or
// asleep with sleep_reason one of idle, idle-timeout, city-stop,
// failed-create, runtime-missing, provider-terminal-error, or
// max-session-age. Sessions parked via `gc session wait` (sleep_reason=wait-hold),
// held by context-churn quarantine, or otherwise signaling "don't touch me"
// keep their slot.
//
// Distinct from `isDrainedSessionBead` because drain-ack can land pool
// workers in state=asleep+sleep_reason=idle when the pre-close ownership
// snapshot falsely reports assigned work. Freeing the slot for idle-asleep
// pool beads lets the supervisor spawn a fresh worker for ready queue work
// instead of stranding it on a ghost slot.
//
// A session parked with sleep_reason=provider-terminal-error is also freeable:
// markProviderTerminalError has classified it as a dead, non-retryable provider
// failure, so its slot must be reaped — otherwise the dead bead and its worktree
// leak indefinitely while still excluded from pool capacity.
//
// sleep_reason=no-wake-reason is freeable, and it is the one that mattered most
// in practice (cr-4phwb1 / T2). The reconciler writes it when a seat has nothing
// to wake for, which is idle by another name — gc's own
// session.IsDeliberateSleepReason already lists it beside idle, idle-timeout and
// drained, and this list is otherwise a near-subset of that one. Leaving it out
// meant a pool could put every seat to sleep for want of work and then hold all
// of its own slots: measured 2026-09-12 on the worker-local nomad lane, five
// seats asleep on no-wake-reason while a P0 routed to that very lane sat
// unclaimed for 12+ minutes, because the pool had no free slot to start a seat
// that could take it. A lane asleep on top of its own queue.
//
// An explicit sleep_reason is still required: deny-by-default for unknown or
// missing reasons so writes that land in state=asleep without a known
// reason (legacy beads, regressions, write races) cannot silently free
// their slot. Deliberate-but-not-disposable reasons (user-hold, wait-hold)
// stay OUT: someone parked those seats on purpose.
func isPoolSessionSlotFreeable(session beads.Bead) bool {
	if isDrainedSessionBead(session) {
		return true
	}
	if strings.TrimSpace(session.Metadata["state"]) != "asleep" {
		return false
	}
	reason := strings.TrimSpace(session.Metadata["sleep_reason"])
	switch reason {
	case string(sessionpkg.SleepReasonIdle), string(sessionpkg.SleepReasonIdleTimeout),
		string(sessionpkg.SleepReasonNoWakeReason),
		string(sessionpkg.SleepReasonCityStop), string(sessionpkg.SleepReasonFailedCreate),
		string(sessionpkg.SleepReasonRuntimeMissing), string(sessionpkg.SleepReasonProviderTerminalError),
		string(sessionpkg.SleepReasonMaxSessionAge):
		return true
	}
	return false
}

// isPoolSessionSlotFreeableInfo is the session.Info mirror of isPoolSessionSlotFreeable.
func isPoolSessionSlotFreeableInfo(i sessionpkg.Info) bool {
	if isDrainedSessionInfo(i) {
		return true
	}
	if strings.TrimSpace(i.MetadataState) != "asleep" {
		return false
	}
	reason := strings.TrimSpace(i.SleepReason)
	switch reason {
	case string(sessionpkg.SleepReasonIdle), string(sessionpkg.SleepReasonIdleTimeout),
		string(sessionpkg.SleepReasonNoWakeReason),
		string(sessionpkg.SleepReasonCityStop), string(sessionpkg.SleepReasonFailedCreate),
		string(sessionpkg.SleepReasonRuntimeMissing), string(sessionpkg.SleepReasonProviderTerminalError),
		string(sessionpkg.SleepReasonMaxSessionAge):
		return true
	}
	return false
}

// poolSlotRepairAuthorised decides whether a DEAD pool-managed seat may have its
// assigned work released and its slot reclaimed.
//
// It exists to break a real deadlock (cr-1jicje) without widening the
// allow-list that isPoolSessionSlotFreeableInfo deliberately keeps narrow:
// closeSessionBeadIfRuntimeStoppedAndUnassigned refuses to close a session bead
// that still HAS assigned work — rightly — while repairStrandedPoolWorkerBead,
// the thing that would unassign that work, was gated on a freeable sleep_reason
// an orphaned seat does not have. Neither side could move.
//
// The extra authority is EVIDENCE, and it is deliberately not available to a
// dormant seat. A session carrying state=asleep is ASSERTING that its box is
// legitimately gone, so runtime absence tells us nothing we did not already
// know — a sleep-capable worker is absent from the runtime precisely because it
// is asleep, and reaping it on that basis retires healthy, resumable seats
// (proven by the scale-check dormancy retention tests). For a seat making no
// such claim — orphaned, or any unrecognised non-dormant state — the box SHOULD
// be up, so a runtime that positively reports it gone is an answer, and a
// stronger one than any inferred sleep reason.
//
// absent is fail-closed: a nil probe, a list error, a partial list or a
// still-present session all answer "not absent", so an unreadable runtime
// widens nothing. A freeable state is never vetoed — the probe may only widen.
func poolSlotRepairAuthorised(freeableState, dormant bool, seat string, absent runtimeAbsenceProbe) bool {
	if freeableState {
		return true
	}
	if dormant {
		return false
	}
	return absent != nil && absent(seat)
}

// isDormantSessionInfo reports whether a session claims deliberate dormancy.
func isDormantSessionInfo(i sessionpkg.Info) bool {
	return strings.TrimSpace(i.MetadataState) == string(sessionpkg.StateAsleep)
}
