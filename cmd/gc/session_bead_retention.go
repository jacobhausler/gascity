package main

import (
	"fmt"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
)

// Closed session beads were the one coordination class with no retention: a
// one_shot lane mints one session bead per incarnation (about one a minute on a
// busy pool) and nothing in gc ever deletes one. Westlands' infra binding
// reached 138k beads of which 44k are closed session beads, and that residue
// alone costs ~1 s of boot and 1.6 GB on disk (cr-4wdtzw).
//
// The retention FIELD already ships. [beads.policies.<use>].delete_after_close
// (config.BeadPolicyConfig) is documented as "deletes matching GC-owned beads
// after they have been closed for this duration", and "session" is already a
// recognized policy name — policyNameForBead classifies a bead carrying
// session.LabelSession or type session.BeadType to it. Only order_tracking was
// ever wired to that field, so setting delete_after_close for the session
// policy deleted nothing. This file wires the second use: the shipped config key
// finally means what its doc says, instead of a new [daemon] ttl invented
// beside it.
const (
	// defaultSessionBeadDeleteAfterClose is the TTL when config carries none.
	// Two days is the age the owner authorized (2026-09-14): past that a
	// session bead is archaeology — the slot it named, any claim it held, and
	// any resumable incarnation it protected are long gone.
	defaultSessionBeadDeleteAfterClose = 48 * time.Hour

	// minSessionBeadDeleteAfterClose clamps a configured TTL. delete_after_close
	// takes whole-duration strings, so a mistyped "1m" would otherwise turn one
	// watchdog run into a history wipe; an hour is the shortest this leg honours.
	minSessionBeadDeleteAfterClose = time.Hour

	// sessionBeadRetentionWatchdogInterval is the minimum time between runs.
	// The order-tracking twin runs every 15 minutes because a stale tracking
	// bead blocks its order; a closed session bead blocks nothing, so this is
	// storage housekeeping on an hourly cadence.
	sessionBeadRetentionWatchdogInterval = time.Hour

	// sessionBeadRetentionWatchdogDeleteBudget bounds deletions per run so a
	// city's first run drains its backlog over hours instead of holding one
	// unbounded mass delete on the tick. At 500 an hour a 44k backlog drains in
	// under four days while every individual run stays a bounded batch.
	sessionBeadRetentionWatchdogDeleteBudget = 500
)

// sessionBeadRetentionPolicyForConfig resolves the closed-session-bead TTL from
// the shipped [beads.policies.session] delete_after_close field, falling back
// to defaultSessionBeadDeleteAfterClose and clamping short values to
// minSessionBeadDeleteAfterClose.
func sessionBeadRetentionPolicyForConfig(cfg *config.City) time.Duration {
	if cfg == nil {
		return defaultSessionBeadDeleteAfterClose
	}
	policy, ok := cfg.Beads.Policies[beadPolicySession]
	if !ok {
		return defaultSessionBeadDeleteAfterClose
	}
	duration := policy.DeleteAfterCloseDuration()
	if duration <= 0 {
		return defaultSessionBeadDeleteAfterClose
	}
	if duration < minSessionBeadDeleteAfterClose {
		return minSessionBeadDeleteAfterClose
	}
	return duration
}

// closedSessionBeadReferenceTime is the timestamp retention compares against:
// when the bead last changed, which for a closed bead is when it closed. The
// zero-updated fallback to CreatedAt matches beads.ListQuery.UpdatedBefore's
// documented legacy semantics, so the store-side filter and this in-memory
// re-check cannot disagree.
func closedSessionBeadReferenceTime(b beads.Bead) time.Time {
	if !b.UpdatedAt.IsZero() {
		return b.UpdatedAt
	}
	return b.CreatedAt
}

// sweepClosedSessionBeads deletes closed session beads closed at or before
// now-ttl, at most limit per call, returning how many went.
//
// Selection is oldest-first so a drain has a deterministic order, and it pushes
// UpdatedBefore into the query so the read stays bounded on a store holding
// tens of thousands of closed beads, with the same predicate re-checked in
// memory because several backends enforce UpdatedBefore Go-side after their own
// limit.
//
// It deletes only beads that are in status closed AND that gc's own classifier
// (policyNameForBead) still calls "session". Both matter: the sessions class
// store is shared with wait gates and other classes' beads, and an open session
// bead is a live slot name, a claim, or a resumable incarnation — deleting one
// is the exact failure this city has already paid for.
func sweepClosedSessionBeads(store beads.Store, now time.Time, ttl time.Duration, limit int) (int, error) {
	if store == nil || limit <= 0 || ttl <= 0 {
		return 0, nil
	}
	cutoff := now.Add(-ttl)
	candidates, err := beads.HandlesFor(store).Live.List(beads.ListQuery{
		Status:        "closed",
		Sort:          beads.SortCreatedAsc,
		UpdatedBefore: cutoff,
		Limit:         limit,
		Live:          true,
	})
	if err != nil {
		return 0, fmt.Errorf("listing closed session beads: %w", err)
	}

	ids := make([]string, 0, len(candidates))
	for _, bead := range candidates {
		if bead.Status != "closed" {
			continue
		}
		if policyNameForBead(bead) != beadPolicySession {
			continue
		}
		if !closedSessionBeadReferenceTime(bead).Before(cutoff) {
			continue
		}
		ids = append(ids, bead.ID)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	// deleteWorkflowBeadsBatch is the graph-aware delete the order-tracking
	// retention prune uses: it unwinds the bead's deps and removes the bead, so
	// the metadata, label, and dep rows go with it (ON DELETE CASCADE on the
	// schema-backed stores), and it collapses into chunked batch deletes where
	// the backend implements beads.BatchDeleter instead of one delete per bead.
	if err := deleteWorkflowBeadsBatch(store, ids); err != nil {
		return 0, fmt.Errorf("deleting closed session beads: %w", err)
	}
	return len(ids), nil
}

// runSessionBeadRetentionWatchdog prunes closed session beads past the session
// bead policy's delete_after_close, at most once per
// sessionBeadRetentionWatchdogInterval and at most
// sessionBeadRetentionWatchdogDeleteBudget beads per run. It is the twin of
// runOrderTrackingRetentionWatchdog and copies its two safety gates: the
// interval stamp, and the backup gate — a mass delete of history runs only
// behind a fresh backup, never on the strength of a TTL alone.
func (cr *CityRuntime) runSessionBeadRetentionWatchdog(now time.Time) {
	if !cr.sessionBeadRetentionWatchdogLast.IsZero() &&
		now.Sub(cr.sessionBeadRetentionWatchdogLast) < sessionBeadRetentionWatchdogInterval {
		return
	}
	cr.sessionBeadRetentionWatchdogLast = now

	// No config means no declared intent to delete history; the twin's nil-cfg
	// leg skips for the same reason.
	if cr.cfg == nil {
		return
	}

	// The cityPath guard is a test affordance: real controllers always set it,
	// so the backup check always runs in production.
	if cr.cityPath != "" {
		if safe, reason := doctor.BulkDeleteSafe(cr.cityPath, cr.cfg, bulkDeleteMaxAge(cr.cfg), now); !safe {
			if cr.stderr != nil {
				fmt.Fprintf(cr.stderr, "%s: session-bead retention watchdog: skipping bulk delete — %s\n", cr.logPrefix, reason) //nolint:errcheck // best-effort stderr
			}
			return
		}
	}

	ttl := sessionBeadRetentionPolicyForConfig(cr.cfg)
	deleted, err := sweepClosedSessionBeads(cr.sessionsBeadStore().Store, now, ttl, sessionBeadRetentionWatchdogDeleteBudget)
	if err != nil && cr.stderr != nil {
		fmt.Fprintf(cr.stderr, "%s: session-bead retention watchdog: %v\n", cr.logPrefix, err) //nolint:errcheck // best-effort stderr
	}
	if deleted > 0 && cr.stderr != nil {
		fmt.Fprintf(cr.stderr, "%s: session-bead retention watchdog: pruned %d closed session bead(s)\n", cr.logPrefix, deleted) //nolint:errcheck // best-effort stderr
	}
}
