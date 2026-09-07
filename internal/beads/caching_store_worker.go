package beads

import "time"

// ClaimAs forwards an assignee-scoped claim to the backing store and refreshes
// the cached bead only when the claim actually won. The wrapper deliberately
// owns no CAS logic of its own: the single-winner guarantee is the backing
// store's, and re-deriving it here would give the cache a second, weaker
// implementation of the contract the conformance suite pins.
//
// A conflict (ok=false) leaves the cache untouched — nothing was written, so
// there is nothing to absorb, and marking the entry dirty would turn every
// lost race into a redundant re-read.
func (c *CachingStore) ClaimAs(id, assignee string) (Bead, bool, error) {
	claimed, ok, err := ClaimFor(c.backing, id, assignee)
	if err != nil || !ok {
		return claimed, ok, err
	}
	c.absorbClaimedBead(id, claimed)
	return claimed, true, nil
}

// absorbClaimedBead folds a won claim into the cache. The claim response is
// the authoritative post-write row, so it is absorbed directly rather than
// re-read; a store that returned an empty envelope instead falls back to
// marking the entry dirty so the next read re-fetches.
func (c *CachingStore) absorbClaimedBead(id string, claimed Bead) {
	fresh, refreshed := c.refreshBeadAfterWrite(id, "refresh bead after claim")
	c.mu.Lock()
	c.noteLocalMutationLocked(id)
	var updated Bead
	notify := false
	switch {
	case refreshed:
		c.absorbFreshLocked(id, fresh, time.Now(), absorbOpts{
			depsMode:   depsFromFields,
			seqMode:    seqKeep,
			clearDirty: true,
		})
		updated = cloneBead(fresh)
		notify = true
	case claimed.ID != "":
		c.absorbFreshLocked(id, claimed, time.Now(), absorbOpts{
			depsMode:   depsKeepCached,
			seqMode:    seqKeep,
			clearDirty: false,
		})
		c.markDirtyLocked(id)
		updated = cloneBead(claimed)
		notify = true
	default:
		c.markDirtyLocked(id)
	}
	c.clearDependentReadyProjectionsLocked(id)
	c.updateStatsLocked()
	c.mu.Unlock()
	if notify {
		c.notifyChange("bead.updated", updated)
	}
}

// Comment forwards an append-only comment to the backing store. Comments live
// outside the cached bead row, so nothing is absorbed and no entry is
// invalidated: a comment changes no field this cache serves.
func (c *CachingStore) Comment(id, text string) error {
	return CommentOn(c.backing, id, text)
}
