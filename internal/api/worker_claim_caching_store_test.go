// Capability-dispatch acceptance for the worker claim route: the resolved
// store is normally a wrapper around the real backend, and the route must
// reach the claim through the native seam (internal/beads.ClaimFor) rather than
// an interface it restates locally. cr-gdeav.5.10: on pin e6c792597 the route
// asserted a two-argument Claim that beads.CachingStore does not carry, so the
// city's own cached work store answered 501 to `gc bd update --claim`.
package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// claimlessFenceableDraft hides both claim shapes behind the Store interface
// and re-exposes the write-fencing capabilities through the shipped handle
// providers, so the only capability it lacks is the claim. Both seams are
// separate on purpose (internal/beads/beads.go:355, metadata_cas.go:83): the
// session-pointer CAS and the lease fence must stay satisfiable here, or the
// route refuses for a reason this test is not about.
type claimlessFenceableDraft struct {
	beads.Store
	base beads.Store
}

func (d claimlessFenceableDraft) ConditionalWriterHandle() (beads.ConditionalWriter, bool) {
	return beads.ConditionalWriterFor(d.base)
}

func (d claimlessFenceableDraft) MetadataCASWriterHandle() (beads.MetadataCASWriter, bool) {
	return beads.MetadataCASWriterFor(d.base)
}

// postWorkerClaim sends one claim body to the real route and records the reply.
func postWorkerClaim(t *testing.T, state State, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := newPostRequest(cityURL(state, "/worker/claim"), strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestWorkerClaimReachesCachingStoreClaimAs pins both halves of the capability
// boundary for the cached store the city actually answers with
// (cmd/gc/api_state.go:305 wraps the work store in beads.NewCachingStore):
// the wrapper's forwarded claim is reached and lands, and a backend behind the
// wrapper that has no claim shape is still a typed 501 that wrote nothing.
func TestWorkerClaimReachesCachingStoreClaimAs(t *testing.T) {
	t.Run("cached store dispatches to the backing claim", func(t *testing.T) {
		backing := &claimingMemStoreDraft{MemStore: beads.NewMemStore()}
		cache := beads.NewCachingStoreForTest(backing, nil)
		created, err := cache.Create(beads.Bead{Title: "cached claim", Status: "open"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		state := newFakeState(t)
		state.stores = map[string]beads.Store{"myrig": cache}
		state.cityBeadStore = cache
		h := newTestCityHandler(t, state)

		rec := postWorkerClaim(t, state, h, workerClaimBody(t, state, created.ID))
		if rec.Code != http.StatusOK {
			t.Fatalf("claim through the caching store = %d, want 200, body: %s", rec.Code, rec.Body.String())
		}

		// The route reached the cache, and the cache forwarded to the backend:
		// one claim call naming this bead and this assignee.
		if len(backing.claimCalls) != 1 {
			t.Fatalf("backend claim calls = %v, want exactly one", backing.claimCalls)
		}
		if want := created.ID + "|worker-local-3-pool"; backing.claimCalls[0] != want {
			t.Errorf("backend claim call = %q, want %q — the claim did not land on the named identity", backing.claimCalls[0], want)
		}
		stored, err := cache.Get(created.ID)
		if err != nil {
			t.Fatalf("cache Get: %v", err)
		}
		if stored.Assignee != "worker-local-3-pool" || stored.Status != "in_progress" {
			t.Errorf("claimed bead = assignee %q status %q, want the worker in_progress", stored.Assignee, stored.Status)
		}
		if stored.Metadata[beadmeta.LeaseOwnerMetadataKey] != "worker-local-3-pool" {
			t.Errorf("lease owner = %q, want the claiming worker — a claim that never stamps its lease is invisible to every reaper", stored.Metadata[beadmeta.LeaseOwnerMetadataKey])
		}
	})

	t.Run("cached store over a claim-less backend refuses and writes nothing", func(t *testing.T) {
		// The cache itself satisfies ActorClaimer by forwarding, so the
		// pre-write probe passes and the absent capability surfaces from the
		// backend mid-claim. It must still be the same typed 501, with the
		// session reservation this request made unwound.
		//
		// MemStore carries Claim of its own (internal/beads/memstore.go:780),
		// so the backend must hide both claim shapes while KEEPING the
		// conditional write: embedding the Store interface drops the fence too
		// and the route then refuses for the wrong capability. The handle
		// provider is the shipped seam for exactly this (beads.go:355).
		base := beads.NewMemStore()
		created, err := base.Create(beads.Bead{Title: "uncacheable claim", Status: "open"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		cache := beads.NewCachingStoreForTest(claimlessFenceableDraft{Store: base, base: base}, nil)

		state := newFakeState(t)
		state.stores = map[string]beads.Store{"myrig": cache}
		state.cityBeadStore = cache
		h := newTestCityHandler(t, state)

		rec := postWorkerClaim(t, state, h, workerClaimBody(t, state, created.ID))
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("claim on a backend with no claim shape = %d, want 501, body: %s", rec.Code, rec.Body.String())
		}
		stored, err := base.Get(created.ID)
		if err != nil {
			t.Fatalf("store Get: %v", err)
		}
		if stored.Assignee != "" || stored.Status != "open" {
			t.Fatalf("refused claim wrote to the bead: assignee=%q status=%q", stored.Assignee, stored.Status)
		}
		sess, err := state.SessionsBeadStore().Get(workerSessionID(t, state))
		if err != nil {
			t.Fatalf("session Get: %v", err)
		}
		if held := sess.Metadata[beadmeta.CurrentClaimBeadIDMetadataKey]; held == created.ID {
			t.Errorf("the 501 left the session pointing at %s, which it does not hold — a pointer naming unheld work outlives the refusal", created.ID)
		}
	})
}
