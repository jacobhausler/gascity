package api

import "github.com/gastownhall/gascity/internal/beads"

// The worker family is the API surface a session that runs off the
// controller's host uses to do a worker's job: take a routed bead, give it
// back, report what it did, and acknowledge a drain. Every operation here
// delegates to a store primitive that already exists and is already pinned by
// a conformance suite — the routes are transport for those primitives, never a
// second implementation of them.
//
// The family is named /worker rather than /hook because
// /v0/city/{cityName}/hook/ is the inbound webhook receiver: a deliberately
// non-Huma surface that reads a raw body for signature verification
// (supervisor.go, and the guard test that keeps it out of the typed layer).
// Reusing that prefix would collide with it.

// workerSessionBody is the request shape shared by claim / heartbeat / release:
// the identity asking, the bead it names, and the session it acts for. The
// identity — not the transport credential — is what the store compares, which
// is what keeps release-if-current a compare-and-set rather than a name check.
type workerSessionBody struct {
	SessionID    string `json:"session_id,omitempty" doc:"gc session the verb is issued for; recorded for attribution, never used as the ownership pointer."`
	Assignee     string `json:"assignee" doc:"Claimant identity (a pool seat or crew holder name). This is the value the store compares." minLength:"1"`
	BeadID       string `json:"bead_id" doc:"Bead the verb acts on." minLength:"1"`
	CurrentClaim string `json:"current_claim,omitempty" doc:"Current-claim pointer the caller observed before claiming; it fences session-pointer reservation."`
}

// WorkerClaimInput is the Huma input for POST /v0/city/{cityName}/worker/claim.
type WorkerClaimInput struct {
	CityScope
	Body workerSessionBody
}

// WorkerClaimOutput is the 200 response for a won claim. The claimed bead is
// returned whole so the caller does not need a follow-up read to learn the
// revision it must echo back on a later conditional update.
type WorkerClaimOutput struct {
	Index       uint64 `header:"X-GC-Index" doc:"Latest event sequence number."`
	ETag        string `header:"ETag" doc:"Precondition token for this holder's snapshot; echo it in a conditional write. Absent when the backing store exposes no usable revision."`
	XGCRevision string `header:"X-GC-Revision" doc:"Store revision the ETag encodes, in plain form."`
	Body        struct {
		Status string     `json:"status" doc:"Claim result." example:"claimed"`
		Bead   beads.Bead `json:"bead" doc:"The claimed bead as the store persisted it."`
	}
}

// WorkerReleaseInput is the Huma input for DELETE /v0/city/{cityName}/worker/claim.
type WorkerReleaseInput struct {
	CityScope
	Body workerSessionBody
}

// WorkerReleaseOutput is the 200 response for a release attempt. A release
// that found the snapshot moved is "skipped", not an error — that is the
// ReleaseIfCurrent contract, and a caller unwinding its own claim must be able
// to tell "I gave it back" from "someone else already holds it" without
// treating the second as a failure.
type WorkerReleaseOutput struct {
	Index uint64 `header:"X-GC-Index" doc:"Latest event sequence number."`
	Body  struct {
		Status string     `json:"status" doc:"Release result: released when the CAS applied, skipped when the bead no longer named the expected holder." example:"released" enum:"released,skipped"`
		Bead   beads.Bead `json:"bead" doc:"The bead after the release attempt."`
	}
}

// WorkerCurrentInput is the Huma input for GET /v0/city/{cityName}/worker/current.
type WorkerCurrentInput struct {
	CityScope
	SessionID string `query:"session_id" doc:"Session bead ID whose current claim is read." minLength:"1" required:"true"`
}

// WorkerCurrentOutput is the 200 response for the current-claim read. An
// unclaimed session answers 200 with an empty bead_id rather than a 404: "this
// session has claimed nothing" is a fact about a session that exists, and the
// caller's own exit contract — not the transport — decides whether that is a
// failure.
type WorkerCurrentOutput struct {
	Index uint64 `header:"X-GC-Index" doc:"Latest event sequence number."`
	Body  struct {
		BeadID string `json:"bead_id" doc:"Work bead the session most recently claimed, or empty when it has claimed nothing."`
	}
}

// WorkerDrainAckInput is the Huma input for POST /v0/city/{cityName}/worker/drain-ack.
type WorkerDrainAckInput struct {
	CityScope
	Body struct {
		SessionID string `json:"session_id" doc:"Session bead ID acknowledging its own drain." minLength:"1"`
	}
}

// WorkerDrainAckOutput is the 200 response for a drain acknowledgement.
type WorkerDrainAckOutput struct {
	Index uint64 `header:"X-GC-Index" doc:"Latest event sequence number."`
	Body  struct {
		Status   string `json:"status" doc:"Acknowledgement result." example:"acknowledged"`
		Released string `json:"released,omitempty" doc:"Work bead the acknowledgement attempted to hand back, when the session's pointer still named one."`
		// ReleaseStatus keeps "we gave the bead back" distinct from "the bead had
		// already moved on", which released alone cannot express: both leave the
		// same bead id in it.
		ReleaseStatus string `json:"release_status,omitempty" doc:"Outcome of the release the acknowledgement attempted: released when the CAS applied, skipped when the bead no longer named this session's identity. Empty when the session held nothing." enum:"released,skipped"`
	}
}

// WorkerHeartbeatInput is the Huma input for POST /v0/city/{cityName}/worker/heartbeat.
type WorkerHeartbeatInput struct {
	CityScope
	Body workerSessionBody
}

// workerLeaseScopeBeadMetadata is the only lease scope this route can honestly
// report at this commit: the write lands on the bead's lease metadata, not on
// bd's native lease table. Kept as a named constant because the value is a
// contract a client branches on, not a string in a doc comment.
const workerLeaseScopeBeadMetadata = "bead-metadata"

// WorkerHeartbeatOutput is the heartbeat response. A 200 here is the promise
// that the named identity still held the bead when the city answered and the
// bead's revision moved under a holder-only write — so the response carries the
// scope of what it renewed, and a client that needed bd's lease table extended
// can see in the payload that it did not get that (see
// humaHandleWorkerHeartbeat's contract block).
type WorkerHeartbeatOutput struct {
	Index       uint64 `header:"X-GC-Index" doc:"Latest event sequence number."`
	ETag        string `header:"ETag" doc:"Precondition token for the refreshed snapshot."`
	XGCRevision string `header:"X-GC-Revision" doc:"Store revision the ETag encodes, in plain form."`
	Body        struct {
		Status     string `json:"status" doc:"Heartbeat result." example:"renewed"`
		LeaseScope string `json:"lease_scope" doc:"Which lease this refresh reached. bead-metadata means the bead's gc.lease_owner stamp and revision moved; bd's native lease table (bd reclaim's selector) is NOT reachable from this route."`
		ClaimedAt  string `json:"claimed_at,omitempty" doc:"First-claim instant (gc.claimed_at), RFC3339 UTC. Write-once: a heartbeat reports it and never re-stamps it."`
		LeaseOwner string `json:"lease_owner,omitempty" doc:"Lease holder the refresh re-affirmed (gc.lease_owner)."`
	}
}

// WorkerCloseInput is the Huma input for POST /v0/city/{cityName}/worker/close.
//
// outcome/commit/branch mirror the ADR-0009 work record the local close gate
// enforces (gc.work_outcome / gc.work_commit / gc.work_branch). Reason is
// required by the same discipline that makes an untyped close uncitable.
type WorkerCloseInput struct {
	CityScope
	Body struct {
		SessionID string `json:"session_id,omitempty" doc:"gc session the close is issued for. Enforced: when the bead carries a session stamp, a different session is refused."`
		Assignee  string `json:"assignee" doc:"The holder performing the close (the same identity the claim took). A close without a holder is refused: the record would attribute the work to whatever the caller typed." minLength:"1"`
		BeadID    string `json:"bead_id" doc:"Bead to close." minLength:"1"`
		Outcome   string `json:"outcome" doc:"Typed close disposition: shipped, no-op, blocked or abandoned."`
		Commit    string `json:"commit,omitempty" doc:"Commit that satisfies the close. Required by shipped."`
		Branch    string `json:"branch,omitempty" doc:"Branch the commit must be reachable on. Required by shipped."`
		Reason    string `json:"reason,omitempty" doc:"Why the close is what it is. Required for every disposition except shipped."`
	}
}

// WorkerCloseOutput echoes the record it wrote, so the caller can confirm what
// landed without a second read that could observe a different bead.
type WorkerCloseOutput struct {
	Index uint64 `header:"X-GC-Index" doc:"Latest event sequence number."`
	Body  struct {
		Status string     `json:"status" doc:"Close result: closed, or already_closed when the same holder retries a close whose record already landed." example:"closed"`
		Bead   beads.Bead `json:"bead" doc:"The bead as the atomic terminal write persisted it."`
	}
}

// WorkerCommentInput is the Huma input for POST /v0/city/{cityName}/worker/comment.
type WorkerCommentInput struct {
	CityScope
	Body struct {
		BeadID string `json:"bead_id" doc:"Bead to comment on." minLength:"1"`
		Text   string `json:"text" doc:"Comment body, appended verbatim." minLength:"1"`
	}
}
