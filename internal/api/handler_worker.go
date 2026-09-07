package api

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/gastownhall/gascity/internal/api/apierr"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

// This file holds the worker family: the operations a session performs on its
// own behalf — claim, release, read its current claim, acknowledge a drain,
// close, comment. Every one of them delegates to a store primitive whose
// semantics are already pinned by a conformance suite. The handlers add
// transport and an HTTP status mapping; they add no concurrency control of
// their own, because a second implementation of a compare-and-swap is exactly
// how single-winner claiming stops being single-winner.

// workerSessionFront opens the session front door for the session class,
// mirroring how every other session handler in this package resolves it. It
// returns a typed 503 when no session store is configured, so a worker sees
// "the city cannot answer right now" rather than a nil dereference.
func (s *Server) workerSessionFront() (*session.Store, error) {
	store := s.state.SessionsBeadStore()
	if store.Store == nil {
		return nil, apierr.StoreUnavailable.Msg("no bead store configured")
	}
	return session.NewStore(store), nil
}

// humaHandleWorkerClaim is the Huma-typed handler for
// POST /v0/city/{cityName}/worker/claim.
//
// Ordering mirrors the in-session claim protocol exactly, and for the same
// reason: the session's current-claim pointer is RESERVED before the bead is
// claimed, so a session can never hold a bead the pointer does not name. If
// the claim is then lost, the reservation is unwound with a compare-and-swap
// against the value this request wrote — never an unconditional clear, which
// would erase a later winner's pointer.
//
// The CAS swaps from the pointer this request established as expected, not from
// "" — see workerExpectedClaimPointer. Hardcoding "" made every session with a
// non-empty pointer conflict forever, including one left pointing at a bead that
// closed, which is the ordinary end state of every completed remote claim.
func (s *Server) humaHandleWorkerClaim(_ context.Context, input *WorkerClaimInput) (*WorkerClaimOutput, error) {
	sessionID := strings.TrimSpace(input.Body.SessionID)
	assignee := strings.TrimSpace(input.Body.Assignee)
	beadID := strings.TrimSpace(input.Body.BeadID)
	if sessionID == "" || assignee == "" || beadID == "" {
		return nil, apierr.InvalidRequest.Msg("session_id, assignee, and bead_id are required")
	}

	sessFront, err := s.workerSessionFront()
	if err != nil {
		return nil, err
	}
	store, _, err := s.resolveBeadOwner(beadID)
	if err != nil {
		return nil, err
	}

	expected, err := s.workerExpectedClaimPointer(sessFront, sessionID, assignee, strings.TrimSpace(input.Body.CurrentClaim))
	if err != nil {
		return nil, err
	}

	outcome, err := sessFront.ReserveCurrentClaim(sessionID, expected, beadID)
	if err != nil {
		if errors.Is(err, session.ErrCurrentClaimCASNotAttempted) {
			return nil, apierr.SessionNotFound.Msg("session " + sessionID + " could not be validated: " + err.Error())
		}
		return nil, apierr.Internal.Msg(err.Error())
	}
	if outcome == beads.MetadataCASConflict {
		// The pointer moved between the read above and this CAS — a concurrent
		// claim by the same session. Refusing here is what keeps one session from
		// holding two claims it can only report one of.
		return nil, apierr.ConflictWrongState.Msg("conflict: session " + sessionID + " pointer changed while claiming " + beadID)
	}

	claimed, ok, err := beads.ClaimFor(store, beadID, assignee)
	if err != nil || !ok {
		// Unwind the reservation this request made, and only that one.
		if _, clearErr := sessFront.ClearCurrentClaim(sessionID, beadID); clearErr != nil {
			return nil, apierr.Internal.Msg("claim failed and the session pointer could not be unwound: " + clearErr.Error())
		}
		switch {
		case errors.Is(err, beads.ErrClaimUnsupported):
			return nil, apierr.NotImplemented.Msg("this city's bead store does not support assignee-scoped claims")
		case errors.Is(err, beads.ErrNotFound):
			return nil, apierr.BeadNotFound.Msg("bead " + beadID + " not found")
		case err != nil:
			return nil, apierr.Internal.Msg(err.Error())
		}
		return nil, apierr.ConflictWrongState.Msg("conflict: bead " + beadID + " is held by another assignee or is closed")
	}

	out := &WorkerClaimOutput{Index: s.latestIndex()}
	out.Body.Status = "claimed"
	out.Body.Bead = claimed
	return out, nil
}

// workerExpectedClaimPointer decides the value the session-pointer CAS must
// swap FROM, mirroring what the in-session path does with its invocation
// snapshot (cmd/gc/cmd_hook_claim.go reserveHookCurrentClaim): the expected
// value is the pointer that was actually observed, never a hardcoded "".
//
// An empty stored pointer expects "" and is the common case. A non-empty one
// splits on whether the bead it names is still the session's live work:
//
//   - Live (in_progress under this assignee): the session already holds work it
//     can only report one of, and the claim is refused — the same 409 the
//     hardcoded "" produced, but now for the reason it was meant to catch.
//   - Anything else: the pointer is a leftover, and it becomes the expected
//     value so the CAS displaces it. Without this a session wedges permanently
//     the first time it closes a bead it claimed remotely, because the pointer
//     outlives the work by design — and again whenever the dead-assignee lane
//     reclaims a bead the session's pointer still names.
//
// observed, when the caller sends one, fences the read: a caller that decided to
// claim while looking at a different pointer is acting on a snapshot that has
// since moved, and is refused rather than allowed to overwrite the successor.
func (s *Server) workerExpectedClaimPointer(sessFront *session.Store, sessionID, assignee, observed string) (string, error) {
	pointer, err := sessFront.CurrentClaimBeadID(sessionID)
	if err != nil {
		if errors.Is(err, session.ErrSessionNotFound) || errors.Is(err, beads.ErrNotFound) {
			return "", apierr.SessionNotFound.Msg("session " + sessionID + " not found")
		}
		return "", apierr.Internal.Msg(err.Error())
	}
	pointer = strings.TrimSpace(pointer)
	if observed != "" && observed != pointer {
		return "", apierr.ConflictWrongState.Msg("conflict: session " + sessionID + " pointer moved to " + strconv.Quote(pointer) + " since the caller observed " + strconv.Quote(observed))
	}
	if pointer == "" {
		return "", nil
	}
	if s.workerClaimPointerIsHeldBy(pointer, assignee) {
		return "", apierr.ConflictWrongState.Msg("conflict: session " + sessionID + " already holds a claim on " + pointer)
	}
	return pointer, nil
}

// workerClaimPointerIsHeldBy reports whether the bead a session pointer names is
// still the work this assignee is executing — in_progress AND named to it. That
// is the only shape in which a pointer represents a claim the session actually
// holds.
//
// Every other shape is a leftover: a closed bead (the end state of every
// completed claim), an open one (released back to the pool), one now held by
// someone else (the dead-assignee lane reclaimed it), or one that cannot be
// resolved at all. None of those may block a fresh claim, and the claim they
// would have blocked is decided by the store's own CAS regardless.
func (s *Server) workerClaimPointerIsHeldBy(beadID, assignee string) bool {
	_, b, err := s.resolveBeadOwner(beadID)
	if err != nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(b.Status), "in_progress") {
		return false
	}
	return strings.TrimSpace(b.Assignee) == strings.TrimSpace(assignee)
}

// humaHandleWorkerRelease is the Huma-typed handler for
// DELETE /v0/city/{cityName}/worker/claim.
//
// A release whose snapshot moved is reported as "skipped" with a 200, not as a
// conflict: ReleaseIfCurrent returns false-not-error for that case, and an
// unwinding caller must be able to distinguish it from a transport failure
// without retrying.
func (s *Server) humaHandleWorkerRelease(_ context.Context, input *WorkerReleaseInput) (*WorkerReleaseOutput, error) {
	assignee := strings.TrimSpace(input.Body.Assignee)
	beadID := strings.TrimSpace(input.Body.BeadID)
	if assignee == "" || beadID == "" {
		return nil, apierr.InvalidRequest.Msg("assignee and bead_id are required")
	}
	released, err := s.releaseWorkerClaim(strings.TrimSpace(input.Body.SessionID), assignee, beadID)
	if err != nil {
		return nil, err
	}
	out := &WorkerReleaseOutput{Index: s.latestIndex()}
	if released {
		out.Body.Status = "released"
	} else {
		out.Body.Status = "skipped"
	}
	return out, nil
}

// releaseWorkerClaim performs the conditional release and, when a session is
// named, clears that session's pointer with a CAS against the same bead id.
// The pointer is cleared only after the bead is actually given back, so a
// failed release never leaves a session unable to name the work it still holds.
func (s *Server) releaseWorkerClaim(sessionID, assignee, beadID string) (bool, error) {
	store, _, err := s.resolveBeadOwner(beadID)
	if err != nil {
		return false, err
	}
	releaser, ok := store.(beads.ConditionalAssignmentReleaser)
	if !ok {
		return false, apierr.NotImplemented.Msg("this city's bead store does not support conditional release")
	}
	released, err := releaser.ReleaseIfCurrent(beadID, assignee)
	if err != nil {
		if errors.Is(err, beads.ErrConditionalReleaseUnsupported) {
			return false, apierr.NotImplemented.Msg("this city's bead store does not support conditional release")
		}
		return false, apierr.Internal.Msg(err.Error())
	}
	if !released || sessionID == "" {
		return released, nil
	}
	sessFront, err := s.workerSessionFront()
	if err != nil {
		return false, err
	}
	if _, err := sessFront.ClearCurrentClaim(sessionID, beadID); err != nil {
		return false, apierr.Internal.Msg("bead released but the session pointer could not be cleared: " + err.Error())
	}
	return true, nil
}

// humaHandleWorkerCurrent is the Huma-typed handler for
// GET /v0/city/{cityName}/worker/current.
func (s *Server) humaHandleWorkerCurrent(_ context.Context, input *WorkerCurrentInput) (*WorkerCurrentOutput, error) {
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return nil, apierr.InvalidRequest.Msg("session_id is required")
	}
	sessFront, err := s.workerSessionFront()
	if err != nil {
		return nil, err
	}
	beadID, err := sessFront.CurrentClaimBeadID(sessionID)
	if err != nil {
		if errors.Is(err, session.ErrSessionNotFound) || errors.Is(err, beads.ErrNotFound) {
			return nil, apierr.SessionNotFound.Msg("session " + sessionID + " not found")
		}
		return nil, apierr.Internal.Msg(err.Error())
	}
	out := &WorkerCurrentOutput{Index: s.latestIndex()}
	out.Body.BeadID = beadID
	return out, nil
}

// humaHandleWorkerDrainAck is the Huma-typed handler for
// POST /v0/city/{cityName}/worker/drain-ack.
//
// It performs the same three steps the in-session `gc runtime drain-ack` does,
// in the same order: give back the claim the session still holds, set the
// acknowledgement metadata the reconciler waits on, then poke the controller so
// the drained state is observed on this tick instead of the next patrol.
//
// The release here is scoped to the claim the session's own pointer names.
// The controller-side path additionally fans out over every work leg and
// identity under a time budget; that wider sweep stays where it is, and
// whatever it would have caught is still collected by the dead-assignee lane.
// Acknowledging late would be worse than acknowledging narrowly: the ack is
// the signal the controller is blocked on.
func (s *Server) humaHandleWorkerDrainAck(_ context.Context, input *WorkerDrainAckInput) (*WorkerDrainAckOutput, error) {
	sessionID := strings.TrimSpace(input.Body.SessionID)
	if sessionID == "" {
		return nil, apierr.InvalidRequest.Msg("session_id is required")
	}
	sessFront, err := s.workerSessionFront()
	if err != nil {
		return nil, err
	}
	info, err := sessFront.Get(sessionID)
	if err != nil {
		if errors.Is(err, session.ErrSessionNotFound) || errors.Is(err, beads.ErrNotFound) {
			return nil, apierr.SessionNotFound.Msg("session " + sessionID + " not found")
		}
		return nil, apierr.Internal.Msg(err.Error())
	}

	held, err := sessFront.CurrentClaimBeadID(sessionID)
	if err != nil {
		return nil, apierr.Internal.Msg(err.Error())
	}
	released, releaseStatus := "", ""
	if held = strings.TrimSpace(held); held != "" {
		gaveBack, err := s.releaseWorkerClaim(sessionID, workerDrainAckAssignee(info), held)
		if err != nil {
			return nil, err
		}
		released = held
		// A release the CAS declined is reported as such rather than folded into
		// the same answer as one that applied. The two mean different things to
		// whoever reads the ack — the bead moved on without us versus we handed it
		// back — and a caller that cannot tell them apart cannot tell a clean
		// drain from one that left work behind.
		releaseStatus = "released"
		if !gaveBack {
			releaseStatus = "skipped"
		}
	}

	provider := s.state.SessionProvider()
	if provider == nil {
		return nil, apierr.ServiceUnavailable.Msg("no session provider configured")
	}
	name := strings.TrimSpace(info.SessionName)
	if name == "" {
		name = sessionID
	}
	if err := errors.Join(
		provider.RemoveMeta(name, runtime.DrainReasonMetaKey),
		provider.RemoveMeta(name, runtime.DrainGenerationMetaKey),
		provider.SetMeta(name, runtime.DrainAckSourceMetaKey, runtime.DrainAckSourceAgent),
		provider.SetMeta(name, runtime.DrainAckMetaKey, "1"),
	); err != nil {
		return nil, apierr.Internal.Msg(err.Error())
	}
	s.state.Poke()

	out := &WorkerDrainAckOutput{Index: s.latestIndex()}
	out.Body.Status = "acknowledged"
	out.Body.Released = released
	out.Body.ReleaseStatus = releaseStatus
	return out, nil
}

// workerDrainAckAssignee is the identity a drain-ack release must match. The
// claim was written under the session's runtime identity, so this ladder must
// be the SAME ladder the claim used, rung for rung — a release that guesses a
// different identity silently skips, and the drained session keeps the bead.
//
// The claiming ladder is remoteWorkerIdentities (cmd/gc/remote_worker.go),
// which reads GC_ALIAS, GC_AGENT, GC_SESSION_NAME, and finally the session id
// itself. This reads the same four off the session bead: alias, agent name,
// tmux session name, then the bead id, which is what GC_SESSION_ID carries.
// The last rung is not a formality — a session with no alias, no agent name,
// and no provider session name claims under its id, and before that rung
// existed here its drain-ack released against "" and always skipped.
func workerDrainAckAssignee(info session.Info) string {
	for _, candidate := range []string{info.Alias, info.AgentName, info.SessionName, info.ID} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate
		}
	}
	return ""
}

// humaHandleWorkerClose is the Huma-typed handler for
// POST /v0/city/{cityName}/worker/close. It is the worker-scoped alias of the
// existing bead close, kept in this family so a worker's whole surface is one
// prefix rather than two.
func (s *Server) humaHandleWorkerClose(_ context.Context, input *WorkerCloseInput) (*OKResponse, error) {
	beadID := strings.TrimSpace(input.Body.BeadID)
	if beadID == "" {
		return nil, apierr.InvalidRequest.Msg("bead_id is required")
	}
	store, _, err := s.resolveBeadOwner(beadID)
	if err != nil {
		return nil, err
	}
	if err := store.Close(beadID); err != nil {
		if errors.Is(err, beads.ErrNotFound) {
			return nil, apierr.ConflictConcurrentDelete.Msg("conflict: bead " + beadID + " was deleted concurrently")
		}
		return nil, apierr.Internal.Msg(err.Error())
	}
	resp := &OKResponse{}
	resp.Body.Status = "closed"
	return resp, nil
}

// humaHandleWorkerComment is the Huma-typed handler for
// POST /v0/city/{cityName}/worker/comment. Comments are append-only and have
// no route anywhere else in the API today; this is the only one.
func (s *Server) humaHandleWorkerComment(_ context.Context, input *WorkerCommentInput) (*OKResponse, error) {
	beadID := strings.TrimSpace(input.Body.BeadID)
	if beadID == "" {
		return nil, apierr.InvalidRequest.Msg("bead_id is required")
	}
	if strings.TrimSpace(input.Body.Text) == "" {
		return nil, apierr.InvalidRequest.Msg("text is required")
	}
	store, _, err := s.resolveBeadOwner(beadID)
	if err != nil {
		return nil, err
	}
	if err := beads.CommentOn(store, beadID, input.Body.Text); err != nil {
		switch {
		case errors.Is(err, beads.ErrCommentUnsupported):
			return nil, apierr.NotImplemented.Msg("this city's bead store keeps no comment log")
		case errors.Is(err, beads.ErrEmptyComment):
			return nil, apierr.InvalidRequest.Msg("text is required")
		case errors.Is(err, beads.ErrNotFound):
			return nil, apierr.BeadNotFound.Msg("bead " + beadID + " not found")
		}
		return nil, apierr.Internal.Msg(err.Error())
	}
	resp := &OKResponse{}
	resp.Body.Status = "commented"
	return resp, nil
}
