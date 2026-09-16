package main

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// TestWispGCClosurePurgeSkipsClosureWithLiveMember is the regression test for
// cr-ybxfw6 (field instances cr-2b29l, cr-1xcat.3, cr-04mim). A box seat was
// handed a step bead by gc hook --claim and woke into a 404: the step was an
// OPEN, ASSIGNED member of an aged closed root's ownership closure, and the
// closure purge deleted it along with its family. Every read path AND every
// write path then refused the id, so the seat could not work the bead, record
// on it, release it, or close it — a dispatch with no recovery path.
//
// The contract this pins: a closure holding unfinished work is not collectible.
// Not "the live member is spared and the rest goes" — half-deleting a family
// around a live claim is how the tracking row went missing underneath the step
// that was still routed from it.
func TestWispGCClosurePurgeSkipsClosureWithLiveMember(t *testing.T) {
	now := time.Now()
	store := newGCStore([]beads.Bead{
		makeGCBead("mol-live", now.Add(-2*time.Hour), "closed", "molecule"),
		{
			ID:        "mol-live.1",
			Status:    "closed",
			Type:      "task",
			CreatedAt: now.Add(-2 * time.Hour),
			ParentID:  "mol-live",
		},
		{
			// The dispatched step: still in flight, held by a seat.
			ID:        "mol-live.2",
			Status:    "in_progress",
			Type:      "task",
			Assignee:  "worker-local-7-pool",
			CreatedAt: now.Add(-2 * time.Hour),
			ParentID:  "mol-live",
		},
	})
	for _, dep := range [][2]string{{"mol-live.1", "mol-live"}, {"mol-live.2", "mol-live"}} {
		if err := store.DepAdd(dep[0], dep[1], "parent-child"); err != nil {
			t.Fatalf("DepAdd(%s->%s): %v", dep[0], dep[1], err)
		}
	}

	entries, err := closedWispGCEntries(store)
	if err != nil {
		t.Fatalf("closedWispGCEntries: %v", err)
	}

	purged, err := purgeExpiredBeadClosures(store, entries, now, 0)
	if err != nil {
		t.Fatalf("purgeExpiredBeadClosures: %v", err)
	}
	if purged != 0 {
		t.Fatalf("purged = %d, want 0 (the closure holds an in_progress step)", purged)
	}
	if len(store.deletedIDs) != 0 {
		t.Fatalf("deletedIDs = %v, want none", store.deletedIDs)
	}
	for _, id := range []string{"mol-live", "mol-live.1", "mol-live.2"} {
		if _, err := store.Get(id); err != nil {
			t.Fatalf("Get(%s) after the skipped purge: %v", id, err)
		}
	}

	// Once the step finishes, the same sweep must collect the family — the skip
	// is a deferral, not a permanent exemption.
	if err := store.Close("mol-live.2"); err != nil {
		t.Fatalf("Close(mol-live.2): %v", err)
	}
	entries, err = closedWispGCEntries(store)
	if err != nil {
		t.Fatalf("closedWispGCEntries after close: %v", err)
	}
	if purged, err = purgeExpiredBeadClosures(store, entries, now, 0); err != nil {
		t.Fatalf("purgeExpiredBeadClosures after close: %v", err)
	}
	if purged != 1 {
		t.Fatalf("purged = %d, want 1 once the family is terminal", purged)
	}
	assertDeletedIDs(t, store.deletedIDs, "mol-live", "mol-live.1", "mol-live.2")
}

// TestWispGCClosureGuardIgnoresStaleAssigneeOnClosedRows protects the purge from
// over-blocking. gc's close path does NOT clear an assignee — closed beads
// legitimately keep the seat that finished them — so "carries an assignee" is
// not a live-claim signal, and a guard built on it would mark most of a healthy
// closure live and stop the retention backlog from ever draining. Only an
// unfinished STATUS is unfinished work.
func TestWispGCClosureGuardIgnoresStaleAssigneeOnClosedRows(t *testing.T) {
	now := time.Now()
	closed := makeGCBead("mol-stale", now.Add(-2*time.Hour), "closed", "molecule")
	closed.Assignee = "mechanic-2-pool"
	store := newGCStore([]beads.Bead{
		closed,
		{
			ID:        "mol-stale.1",
			Status:    "closed",
			Type:      "task",
			Assignee:  "worker-local-1-pool",
			CreatedAt: now.Add(-2 * time.Hour),
			ParentID:  "mol-stale",
		},
	})
	if err := store.DepAdd("mol-stale.1", "mol-stale", "parent-child"); err != nil {
		t.Fatalf("DepAdd(mol-stale.1->mol-stale): %v", err)
	}

	entries, err := closedWispGCEntries(store)
	if err != nil {
		t.Fatalf("closedWispGCEntries: %v", err)
	}
	purged, err := purgeExpiredBeadClosures(store, entries, now, 0)
	if err != nil {
		t.Fatalf("purgeExpiredBeadClosures: %v", err)
	}
	if purged != 1 {
		t.Fatalf("purged = %d, want 1 (a closed row with a stale assignee is finished work)", purged)
	}
	assertDeletedIDs(t, store.deletedIDs, "mol-stale", "mol-stale.1")
}

// TestWispGCClosureGuardReadsMembersLive is the member-level twin of
// ra-nxppyo: the enumeration can come from a cached snapshot, so a member that
// was open when the snapshot was taken but has since been dispatched (or
// reopened) must still be seen. The guard reads every member through the
// store's live handle; this pins that a member the snapshot does not carry is
// still probed, and an unreadable one refuses the purge instead of deleting
// blind.
func TestWispGCClosureGuardReadsMembersLive(t *testing.T) {
	now := time.Now()
	store := newGCStore([]beads.Bead{
		makeGCBead("mol-dep", now.Add(-2*time.Hour), "closed", "molecule"),
		{
			ID:        "mol-dep.1",
			Status:    "closed",
			Type:      "task",
			CreatedAt: now.Add(-2 * time.Hour),
			ParentID:  "mol-dep",
		},
		{
			ID:        "mol-dep.1.1",
			Status:    "open",
			Type:      "task",
			CreatedAt: now.Add(-2 * time.Hour),
			ParentID:  "mol-dep.1",
		},
	})
	if err := store.DepAdd("mol-dep.1", "mol-dep", "parent-child"); err != nil {
		t.Fatalf("DepAdd(mol-dep.1->mol-dep): %v", err)
	}
	// mol-dep.1.1 below is reached ONLY over the parent-child DEP channel, which
	// carries ids and no bead columns — so its status can only come from a read
	// the collector chooses to make.
	if err := store.DepAdd("mol-dep.1.1", "mol-dep.1", "parent-child"); err != nil {
		t.Fatalf("DepAdd(mol-dep.1.1->mol-dep.1): %v", err)
	}

	entries, err := closedWispGCEntries(store)
	if err != nil {
		t.Fatalf("closedWispGCEntries: %v", err)
	}
	purged, err := purgeExpiredBeadClosures(store, entries, now, 0)
	if err != nil {
		t.Fatalf("purgeExpiredBeadClosures: %v", err)
	}
	if purged != 0 || len(store.deletedIDs) != 0 {
		t.Fatalf("purged = %d, deleted = %v, want 0/none (grandchild is open)", purged, store.deletedIDs)
	}

	// An unreadable member is not evidence of a dead one: the sweep surfaces the
	// read failure rather than deleting the family anyway.
	if err := store.Close("mol-dep.1.1"); err != nil {
		t.Fatalf("Close(mol-dep.1.1): %v", err)
	}
	store.getErrors["mol-dep.1"] = fmt.Errorf("backend down")
	entries, err = closedWispGCEntries(store)
	if err != nil {
		t.Fatalf("closedWispGCEntries: %v", err)
	}
	if _, err = purgeExpiredBeadClosures(store, entries, now, 0); err == nil {
		t.Fatal("purgeExpiredBeadClosures: want error from an unreadable member, got nil")
	}
}

// TestOrderTrackingRetentionSkipsRunHoldingOpenStep is the retention-prune half
// of cr-ybxfw6. The prune deletes the CLOSED RUN BEAD only — dep unwind plus
// one delete, with ON DELETE CASCADE dropping the run's edge rows and not its
// children — so before this guard it could delete the tracking row a seat had
// just been dispatched from while the step it was dispatched to stayed open.
// Retention prunes history; deferring one aged row is free, deleting the row a
// live dispatch names is not.
func TestOrderTrackingRetentionSkipsRunHoldingOpenStep(t *testing.T) {
	now := time.Now()
	aged := now.Add(-48 * time.Hour)
	var seed []beads.Bead
	// minClosedOrderTrackingRetained + 3 aged closed runs of one order: the
	// oldest 3 past the recent-history floor are the prune candidates.
	for i := 0; i < minClosedOrderTrackingRetained+3; i++ {
		seed = append(seed, beads.Bead{
			ID:        fmt.Sprintf("aged-%02d", i),
			Title:     "order:aged",
			Status:    "closed",
			Type:      "task",
			CreatedAt: aged.Add(time.Duration(i) * time.Minute),
			Labels:    []string{"order-run:aged", labelOrderTracking},
			Ephemeral: true,
		})
	}
	// The oldest candidate carries a step that has not finished.
	seed = append(seed, beads.Bead{
		ID:        "aged-00.step",
		Title:     "dispatched step still open",
		Status:    "in_progress",
		Type:      "task",
		Assignee:  "worker-local-1-pool",
		CreatedAt: aged,
		ParentID:  "aged-00",
	})
	store := beads.NewMemStoreFrom(100, seed, nil)
	if err := store.DepAdd("aged-00.step", "aged-00", "parent-child"); err != nil {
		t.Fatalf("DepAdd(aged-00.step->aged-00): %v", err)
	}

	deleted, err := sweepClosedOrderTrackingRetention(store, now, orderTrackingRetentionPolicy{
		deleteAfterClose: time.Hour,
		retainLast:       minClosedOrderTrackingRetained,
	}, nil)
	if err != nil {
		t.Fatalf("sweepClosedOrderTrackingRetention: %v", err)
	}
	if _, err := store.Get("aged-00"); err != nil {
		t.Fatalf("Get(aged-00): %v — the run holding an open step must survive the prune", err)
	}
	// Its two aged siblings carry no live work and must still prune, so the
	// guard is a deferral of one row and not a freeze of the sweep.
	for _, id := range []string{"aged-01", "aged-02"} {
		if _, err := store.Get(id); !errors.Is(err, beads.ErrNotFound) {
			t.Fatalf("Get(%s) err = %v, want ErrNotFound", id, err)
		}
	}
	if want := 2; deleted != want {
		t.Fatalf("deleted = %d, want %d (the aged run with a live step is deferred)", deleted, want)
	}
}

// TestOrderTrackingRetentionPreviewMatchesTheSweep keeps the watchdog's preview
// honest: countClosedOrderTrackingRetentionEligible documents that it "cannot
// drift from what the sweep would delete". If the sweep defers a run whose
// family is still live, the preview must not promise it.
func TestOrderTrackingRetentionPreviewMatchesTheSweep(t *testing.T) {
	now := time.Now()
	aged := now.Add(-48 * time.Hour)
	var seed []beads.Bead
	for i := 0; i < minClosedOrderTrackingRetained+1; i++ {
		seed = append(seed, beads.Bead{
			ID:        fmt.Sprintf("prev-%02d", i),
			Title:     "order:prev",
			Status:    "closed",
			Type:      "task",
			CreatedAt: aged.Add(time.Duration(i) * time.Minute),
			Labels:    []string{"order-run:prev", labelOrderTracking},
			Ephemeral: true,
		})
	}
	seed = append(seed, beads.Bead{
		ID:        "prev-00.step",
		Status:    "open",
		Type:      "task",
		CreatedAt: aged,
		ParentID:  "prev-00",
	})
	store := beads.NewMemStoreFrom(100, seed, nil)
	policy := orderTrackingRetentionPolicy{
		deleteAfterClose: time.Hour,
		retainLast:       minClosedOrderTrackingRetained,
	}

	count, err := countClosedOrderTrackingRetentionEligible([]beads.Store{store}, now, policy, nil)
	if err != nil {
		t.Fatalf("countClosedOrderTrackingRetentionEligible: %v", err)
	}
	if count != 0 {
		t.Fatalf("eligible = %d, want 0 (the only candidate holds an open step)", count)
	}
}
