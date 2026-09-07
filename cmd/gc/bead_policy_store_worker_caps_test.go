package main

import (
	"errors"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// commentingMemStore is a MemStore that also keeps a comment log, so the
// policy wrapper has a backing store that genuinely implements the optional
// beads.Commenter capability. MemStore alone keeps no comment log, which would
// make a "comments work" assertion pass for the wrong reason.
type commentingMemStore struct {
	*beads.MemStore
	comments map[string][]string
}

func newCommentingMemStore() *commentingMemStore {
	return &commentingMemStore{MemStore: beads.NewMemStore(), comments: map[string][]string{}}
}

func (s *commentingMemStore) Comment(id, text string) error {
	if _, err := s.Get(id); err != nil {
		return err
	}
	s.comments[id] = append(s.comments[id], text)
	return nil
}

// TestBeadPolicyStoreForwardsWorkerClaim pins the claim half of the worker
// capability contract through the policy wrapper.
//
// The controller serves EVERY store through wrapStoreWithBeadPolicies
// (cmd/gc/main.go openStoreResultAtForCityWithConfigAndBDContext for the city
// store, api_state.go openRigStore for each rig, and wrapWithCachingStore
// re-wraps after the cache). beadPolicyStore embeds the beads.Store INTERFACE,
// which carries none of the optional capabilities, so an unforwarded
// capability is invisible to beads.ClaimFor's type assertion and the
// /worker/claim route answers 501 for a bead that reads fine.
func TestBeadPolicyStoreForwardsWorkerClaim(t *testing.T) {
	backing := beads.NewMemStore()
	seeded, err := backing.Create(beads.Bead{Title: "claimable"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	store := wrapStoreWithBeadPolicies(backing, &config.City{})

	claimed, ok, err := beads.ClaimFor(store, seeded.ID, "worker-1")
	if errors.Is(err, beads.ErrClaimUnsupported) {
		t.Fatalf("policy-wrapped store hid the backing claim capability: %v", err)
	}
	if err != nil {
		t.Fatalf("ClaimFor: %v", err)
	}
	if !ok {
		t.Fatalf("ClaimFor reported a conflict on an unclaimed bead")
	}
	if claimed.Assignee != "worker-1" {
		t.Fatalf("claimed assignee = %q, want worker-1", claimed.Assignee)
	}
}

// TestBeadPolicyStoreForwardsWorkerComment is the comment half of the same
// contract: /worker/comment maps ErrCommentUnsupported to 501, so a wrapper
// that hides the backing store's comment log makes every remote worker
// comment fail against a city whose ledger keeps one.
func TestBeadPolicyStoreForwardsWorkerComment(t *testing.T) {
	backing := newCommentingMemStore()
	seeded, err := backing.Create(beads.Bead{Title: "commentable"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	store := wrapStoreWithBeadPolicies(backing, &config.City{})

	if err := beads.CommentOn(store, seeded.ID, "hello"); err != nil {
		t.Fatalf("CommentOn through the policy wrapper: %v", err)
	}
	if got := backing.comments[seeded.ID]; len(got) != 1 || got[0] != "hello" {
		t.Fatalf("backing comment log = %v, want [hello]", got)
	}
}

// TestBeadPolicyStoreReportsAbsentWorkerCapabilities keeps the forward honest:
// a backing store that genuinely has no comment log must still report the
// named capability error rather than a nil-interface panic or a silent no-op.
func TestBeadPolicyStoreReportsAbsentWorkerCapabilities(t *testing.T) {
	backing := &policyStoreWithoutWorkerCaps{Store: beads.NewMemStore()}
	seeded, err := backing.Create(beads.Bead{Title: "plain"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	store := wrapStoreWithBeadPolicies(backing, &config.City{})

	if err := beads.CommentOn(store, seeded.ID, "hello"); !errors.Is(err, beads.ErrCommentUnsupported) {
		t.Fatalf("CommentOn error = %v, want ErrCommentUnsupported", err)
	}
	if _, _, err := beads.ClaimFor(store, seeded.ID, "worker-1"); !errors.Is(err, beads.ErrClaimUnsupported) {
		t.Fatalf("ClaimFor error = %v, want ErrClaimUnsupported", err)
	}
}

// policyStoreWithoutWorkerCaps embeds the beads.Store INTERFACE rather than
// *MemStore, so neither optional worker capability is promoted — a backing
// store that really has no claim CAS and no comment log.
type policyStoreWithoutWorkerCaps struct {
	beads.Store
}
