// The capability-shape acceptance for the remote worker verbs (cr-gdeav.5.10.3).
//
// What the city hands these routes is never the leaf store: cmd/gc/api_state.go
// serves CityBeadStore() as the policy layer OVER beads.NewCachingStore, and that
// layer's only conditional-write surface is the shipped
// beads.ConditionalWriterHandleProvider (cmd/gc/bead_policy_store.go
// ConditionalWriterHandle). So the routes must resolve the fence through the
// handle. workerLeaseWriter calls beads.ConditionalWriterFor, and
// beads.ConditionalWriterFor falls back to the handle provider — if it had
// instead kept a bare `store.(beads.ConditionalWriter)` assertion, every verb in
// this family would answer 501 for a city that can fence, which is the incident
// these tests keep from coming back. The production type's own forwarding is
// pinned where it lives (cmd/gc/bead_policy_store_conditional_writer_test.go);
// this file pins the half the handler owns: given a wrapper whose capability
// arrives only through the handle, the claim reaches the backing claim and the
// heartbeat's lease fence reaches the backing writer — and a backing without the
// capability still gets the typed 501 that writes nothing.
package api

import (
	"net/http"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// policyShapeDraft reproduces the SHAPE of cmd/gc's beadPolicyStore, method for
// method, and nothing else: it embeds the beads.Store INTERFACE (so no optional
// capability is promoted), forwards the claim in the ActorClaimer spelling only,
// declares the conditional-writes resolution target, and exposes the fence
// solely through the handle provider. It deliberately implements neither
// AssigneeClaimer (Claim) nor beads.ConditionalWriter — a route that asserts
// either shape locally, or asserts ConditionalWriter instead of consulting
// ConditionalWriterFor, fails here exactly as it did in the city.
type policyShapeDraft struct {
	beads.Store
	base beads.Store
}

var (
	_ beads.ActorClaimer                     = policyShapeDraft{}
	_ beads.ConditionalWriterHandleProvider  = policyShapeDraft{}
	_ beads.ConditionalWritesResolveTargeter = policyShapeDraft{}
)

func (d policyShapeDraft) ClaimAs(id, assignee string) (beads.Bead, bool, error) {
	return beads.ClaimFor(d.base, id, assignee)
}

func (d policyShapeDraft) ConditionalWriterHandle() (beads.ConditionalWriter, bool) {
	return beads.ConditionalWriterFor(d.base)
}

func (d policyShapeDraft) ConditionalWritesResolveTarget() beads.Store { return d.base }

// TestWorkerVerbsReachBackingThroughHandleOnlyStore proves the two claims of the
// bead: the remote claim can reach beads.ClaimFor, and the heartbeat's lease
// fencing can reach the writer behind the handle.
func TestWorkerVerbsReachBackingThroughHandleOnlyStore(t *testing.T) {
	backing := &claimingMemStoreDraft{MemStore: beads.NewMemStore()}
	cache := beads.NewCachingStoreForTest(backing, nil)
	shape := policyShapeDraft{Store: cache, base: cache}
	created, err := cache.Create(beads.Bead{Title: "policy wrapped", Status: "open"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	state := newFakeState(t)
	state.stores = map[string]beads.Store{"myrig": shape}
	state.cityBeadStore = shape
	h := newTestCityHandler(t, state)

	// Claim: dispatched through beads.ClaimFor, reached the backend's own claim.
	workerClaim(t, h, state, created.ID)
	if len(backing.claimCalls) != 1 || backing.claimCalls[0] != created.ID+"|worker-local-3-pool" {
		t.Fatalf("backend claim calls = %v, want one claim of this bead by the named identity — the route stopped at the wrapper", backing.claimCalls)
	}
	claimedAtBacking, err := backing.Get(created.ID)
	if err != nil {
		t.Fatalf("backing Get: %v", err)
	}
	if claimedAtBacking.Metadata[beadmeta.LeaseOwnerMetadataKey] != "worker-local-3-pool" {
		t.Fatalf("lease never reached the backing through the handle: %v", claimedAtBacking.Metadata)
	}
	claimRevision := claimedAtBacking.Revision

	// Heartbeat: the stamp is a revision-fenced write that must arrive AT THE
	// BACKING. Revision 0 (or an unchanged ledger) would mean the route answered
	// liveness without the fenced write ever reaching the store.
	before, err := backing.Get(created.ID)
	if err != nil {
		t.Fatalf("backing Get before heartbeat: %v", err)
	}
	rec := workerHeartbeat(t, h, state, created.ID, workerSessionID(t, state), "worker-local-3-pool")
	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat through the handle-only store = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	after, err := backing.Get(created.ID)
	if err != nil {
		t.Fatalf("backing Get after heartbeat: %v", err)
	}
	if after.Revision <= before.Revision {
		t.Fatalf("heartbeat answered 200 but the backing revision stayed %d (claim left it at %d): the fenced stamp never reached the writer behind the handle", after.Revision, claimRevision)
	}
	if after.Metadata[beadmeta.LeaseOwnerMetadataKey] != "worker-local-3-pool" {
		t.Fatalf("heartbeat landed on the backing with the wrong holder: %v", after.Metadata)
	}
	if after.Metadata[beadmeta.ClaimedAtMetadataKey] != before.Metadata[beadmeta.ClaimedAtMetadataKey] || after.Metadata[beadmeta.ClaimedAtMetadataKey] == "" {
		t.Fatalf("heartbeat moved the write-once %s: %q -> %q", beadmeta.ClaimedAtMetadataKey, before.Metadata[beadmeta.ClaimedAtMetadataKey], after.Metadata[beadmeta.ClaimedAtMetadataKey])
	}

	// The fence still fences: a stranger's heartbeat is refused and the backing
	// does not move at all.
	revBeforeStranger := after.Revision
	stranger := workerHeartbeat(t, h, state, created.ID, workerSessionID(t, state), "some-other-pool")
	if stranger.Code == http.StatusOK {
		t.Fatal("a non-holder heartbeat was accepted through the handle-only store")
	}
	if moved, err := backing.Get(created.ID); err != nil || moved.Revision != revBeforeStranger {
		t.Fatalf("the refused heartbeat still wrote through the handle: revision %d -> %d (err %v)", revBeforeStranger, moved.Revision, err)
	}
}

// TestWorkerClaimRefusesHandleLessPolicyStore is the fail-closed half: the same
// wrapper shape over a backing with no conditional write at all must keep the
// typed 501, refuse BEFORE writing anything, and leave the session pointer alone.
func TestWorkerClaimRefusesHandleLessPolicyStore(t *testing.T) {
	base := beads.NewMemStore()
	created, err := base.Create(beads.Bead{Title: "no fence behind the wrapper", Status: "open"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// No cache between the wrapper and a leaf that hides the writer: the handle
	// has nothing to delegate to, so the shape genuinely has no capability.
	shape := policyShapeDraft{Store: conditionalWriterHiddenDraft{Store: base}, base: conditionalWriterHiddenDraft{Store: base}}

	state := newFakeState(t)
	state.stores = map[string]beads.Store{"myrig": shape}
	state.cityBeadStore = shape
	h := newTestCityHandler(t, state)

	rec := postWorkerClaim(t, state, h, workerClaimBody(t, state, created.ID))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("claim on an unfenceable store = %d, want 501, body: %s", rec.Code, rec.Body.String())
	}
	stored, err := base.Get(created.ID)
	if err != nil {
		t.Fatalf("backing Get: %v", err)
	}
	if stored.Assignee != "" || stored.Status != "open" || stored.Metadata[beadmeta.LeaseOwnerMetadataKey] != "" {
		t.Fatalf("the 501 wrote to the bead anyway: assignee=%q status=%q metadata=%v", stored.Assignee, stored.Status, stored.Metadata)
	}

	// Same store, same refusal, no write: heartbeat.
	hb := workerHeartbeat(t, h, state, created.ID, workerSessionID(t, state), "worker-local-3-pool")
	if hb.Code != http.StatusNotImplemented {
		t.Fatalf("heartbeat on an unfenceable store = %d, want 501, body: %s", hb.Code, hb.Body.String())
	}
}

// conditionalWriterHiddenDraft embeds the beads.Store INTERFACE so the leaf's
// UpdateIfMatch / CompareAndSetMetadataKey are not promoted: a backing that
// genuinely cannot fence a write.
type conditionalWriterHiddenDraft struct {
	beads.Store
}
