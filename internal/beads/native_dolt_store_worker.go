package beads

import (
	"context"
	"errors"
	"fmt"
	"strings"

	beadslib "github.com/steveyegge/beads"
	"github.com/steveyegge/beads/beadserrors"
	"github.com/steveyegge/beads/issueops"
)

// The native Dolt store is the WORK store a rig-backed city resolves, so it —
// not just the in-process stores the unit tests reach for — has to carry the
// worker capabilities ClaimFor/CommentOn discover. Without these assertions a
// deployment whose [storage.classes] work class is native Dolt answers every
// /worker/claim and /worker/comment with a 501 while the test suite stays green.
var (
	_ AssigneeClaimer = (*NativeDoltStore)(nil)
	_ Commenter       = (*NativeDoltStore)(nil)
)

// Claim atomically claims a bead for assignee, with the semantics
// [SQLiteStore.Claim] states normatively: it succeeds (status -> in_progress,
// assignee -> caller) only while the bead is open/in_progress and unassigned,
// is idempotent for the current holder, returns ok=false — a conflict, not an
// error — when another assignee holds it or it is closed, and ErrNotFound when
// the bead does not exist.
//
// Single-winner comes from the compare-and-set living entirely inside one
// native transaction: the read of the current assignee, the decision, and the
// write commit together, so a competing claimant either observes the bead
// unassigned before this transaction commits (and then loses its own commit to
// the serialization conflict, which the retry re-drives against the now-claimed
// row) or observes it already held. The upstream storage layer exposes no
// conditional `UPDATE ... WHERE assignee IS NULL` primitive and no raw-SQL
// escape hatch, so the transaction is the only composition point available —
// the same argument [NativeDoltStore.CompareAndSetMetadataKey] and
// [NativeDoltStore.ReleaseIfCurrent] (the release-dual of this method) make.
//
// The returned bead is built from the in-transaction row, so it carries the
// post-write status and assignee. Its Deps are not hydrated (tx.GetIssue does
// not carry dependency records) and its ClaimFence is the native store's
// existing zero — that fence is a SQLite kv-table mechanism this backend has
// never implemented, and a claim is not the place to introduce it.
func (s *NativeDoltStore) Claim(id, assignee string) (Bead, bool, error) {
	assignee = strings.TrimSpace(assignee)
	if assignee == "" {
		return Bead{}, false, fmt.Errorf("claiming bead %q: empty assignee", id)
	}
	storage, release, err := s.acquireStorage()
	if err != nil {
		return Bead{}, false, err
	}
	defer release()

	var claimed Bead
	var ok bool
	err = retryOnNativeDoltSerializationConflict(func() error {
		ctx, cancel := nativeDoltOperationContext(context.TODO())
		defer cancel()
		return storage.RunInTransaction(ctx, fmt.Sprintf("gc: claim bead %s for %s", id, assignee), func(tx beadslib.Transaction) error {
			// Upstream may replay this callback after a retryable commit
			// failure, and the outer retry re-drives it too. The result
			// belongs to the attempt that commits, never to an earlier one:
			// otherwise an attempt that reached UpdateIssue but did not commit
			// would leave ok=true while the row stayed unclaimed.
			claimed, ok = Bead{}, false
			issue, err := tx.GetIssue(ctx, id)
			if err != nil {
				err = nativeStoreError(id, err)
				if errors.Is(err, ErrNotFound) {
					return fmt.Errorf("claiming bead %q: %w", id, ErrNotFound)
				}
				return err
			}
			if issue == nil {
				return fmt.Errorf("claiming bead %q: %w", id, ErrNotFound)
			}
			if issue.Status != beadslib.StatusOpen && issue.Status != beadslib.StatusInProgress {
				// Terminal and otherwise non-claimable states are never
				// resurrected. Committing an empty transaction leaves ok
				// false, which the caller reads as a conflict.
				return nil
			}
			current := strings.TrimSpace(issue.Assignee)
			if current != "" && current != assignee {
				// Held by another worker — conflict, not an error.
				return nil
			}
			if current == assignee && issue.Status == beadslib.StatusInProgress {
				// Same-owner reclaims are true no-ops: no write, so no
				// revision is consumed, and the stored snapshot is returned.
				bead, err := beadFromNativeIssue(issue)
				if err != nil {
					return err
				}
				claimed, ok = bead, true
				return nil
			}
			if err := tx.UpdateIssue(ctx, id, map[string]interface{}{
				"status":   "in_progress",
				"assignee": assignee,
			}, s.actor); err != nil {
				return nativeStoreError(id, err)
			}
			final, err := tx.GetIssue(ctx, id)
			if err != nil {
				return nativeStoreError(id, err)
			}
			if final == nil {
				return fmt.Errorf("claiming bead %q: re-reading claimed row: %w", id, ErrNotFound)
			}
			bead, err := beadFromNativeIssue(final)
			if err != nil {
				return err
			}
			claimed, ok = bead, true
			return nil
		})
	})
	if err != nil {
		return Bead{}, false, err
	}
	return claimed, ok, nil
}

// Comment appends text to the bead's comment log, the same append-only log
// `bd comment` (and therefore [BdStore.Comment]) writes, attributed to this
// store's actor. There is no edit or delete, matching the log itself.
//
// An empty comment is refused rather than written, so a caller that lost its
// message body learns that instead of seeing a successful no-op — the same
// refusal BdStore makes.
//
// The append goes through the backend's Commenter role, which the upstream
// contract states is ONE atomic mutation with exactly one history entry and
// which raises its own ErrNotFound for an id naming neither an issue nor a
// wisp — so this method needs no existence pre-read. The obvious alternative,
// composing GetIssue + AddComment inside RunInTransaction, does not work on a
// real backend at all: the embedded transaction answers "embeddedTransaction:
// AddComment not implemented", which every in-memory double would have hidden.
//
// The call is deliberately NOT wrapped in
// retryOnNativeDoltSerializationConflict: an append is not idempotent, and
// replaying one whose outcome is unknown would risk a duplicate entry in a log
// that offers no way to remove it.
func (s *NativeDoltStore) Comment(id, text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("commenting on bead %q: %w", id, ErrEmptyComment)
	}
	storage, release, err := s.acquireStorage()
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := nativeDoltOperationContext(context.TODO())
	defer cancel()

	commenter, err := storage.Commenter()
	if err != nil {
		return fmt.Errorf("commenting on bead %q: %w", id, err)
	}
	if _, err := commenter.AddComment(ctx, issueops.AddCommentRequest{
		Author:  s.actor,
		IssueID: id,
		Text:    text,
	}); err != nil {
		if errors.Is(err, beadserrors.ErrNotFound) {
			return fmt.Errorf("commenting on bead %q: %w", id, ErrNotFound)
		}
		return fmt.Errorf("commenting on bead %q: %w", id, nativeStoreError(id, err))
	}
	return nil
}
