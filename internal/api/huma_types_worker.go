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

// WorkerClaimInput is the Huma input for POST /v0/city/{cityName}/worker/claim.
type WorkerClaimInput struct {
	CityScope
	Body struct {
		SessionID string `json:"session_id" doc:"Session bead ID of the claiming session. The session's current-claim pointer is reserved before the claim and released again if the claim is lost." minLength:"1"`
		Assignee  string `json:"assignee" doc:"Identity the bead is claimed for. This is the claim actor, not the transport identity." minLength:"1"`
		BeadID    string `json:"bead_id" doc:"Work bead to claim." minLength:"1"`
		// CurrentClaim is the pointer snapshot the caller read before it decided
		// to claim. It is the CAS fence: a caller that acted on a pointer which
		// has since moved is refused rather than allowed to overwrite whatever
		// replaced it. Optional, because the server derives an expected value on
		// its own for a caller that does not send one.
		CurrentClaim string `json:"current_claim,omitempty" doc:"Current-claim pointer the caller observed before claiming, echoed back as the compare-and-swap's expected value. Omit when the caller did not read one; the server then derives the expected value from the stored pointer."`
	}
}

// WorkerClaimOutput is the 200 response for a won claim. The claimed bead is
// returned whole so the caller does not need a follow-up read to learn the
// revision it must echo back on a later conditional update.
type WorkerClaimOutput struct {
	Index uint64 `header:"X-GC-Index" doc:"Latest event sequence number."`
	Body  struct {
		Status string     `json:"status" doc:"Claim result." example:"claimed"`
		Bead   beads.Bead `json:"bead" doc:"The claimed bead as the store persisted it."`
	}
}

// WorkerReleaseInput is the Huma input for DELETE /v0/city/{cityName}/worker/claim.
type WorkerReleaseInput struct {
	CityScope
	Body struct {
		SessionID string `json:"session_id,omitempty" doc:"Session bead ID whose current-claim pointer is cleared alongside the release. Optional: a release with no session named touches only the work bead."`
		Assignee  string `json:"assignee" doc:"Expected current assignee. The release is applied only while the bead still names this holder." minLength:"1"`
		BeadID    string `json:"bead_id" doc:"Work bead to release." minLength:"1"`
	}
}

// WorkerReleaseOutput is the 200 response for a release attempt. A release
// that found the snapshot moved is "skipped", not an error — that is the
// ReleaseIfCurrent contract, and a caller unwinding its own claim must be able
// to tell "I gave it back" from "someone else already holds it" without
// treating the second as a failure.
type WorkerReleaseOutput struct {
	Index uint64 `header:"X-GC-Index" doc:"Latest event sequence number."`
	Body  struct {
		Status string `json:"status" doc:"Release result: released when the CAS applied, skipped when the bead no longer named the expected holder." example:"released" enum:"released,skipped"`
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

// WorkerCloseInput is the Huma input for POST /v0/city/{cityName}/worker/close.
type WorkerCloseInput struct {
	CityScope
	Body struct {
		BeadID string `json:"bead_id" doc:"Work bead to close." minLength:"1"`
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
