package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// sessionRetentionBead builds a session bead as the reconciler writes one:
// type session plus the gc:session label, which is what policyNameForBead
// classifies into the "session" bead policy.
func sessionRetentionBead(id, status string, created, updated time.Time) beads.Bead {
	return beads.Bead{
		ID:        id,
		Title:     "session " + id,
		Status:    status,
		Type:      sessionpkg.BeadType,
		Labels:    []string{sessionpkg.LabelSession},
		CreatedAt: created,
		UpdatedAt: updated,
		Metadata:  map[string]string{"session_name": id},
	}
}

func seedSessionBeadRetentionCity(t *testing.T, now time.Time, backupAge time.Duration, extra ...beads.Bead) (*CityRuntime, beads.Store, *bytes.Buffer) {
	t.Helper()
	cityDir := t.TempDir()
	backupDir := filepath.Join(cityDir, ".beads", "backup")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", backupDir, err)
	}
	stateJSON := fmt.Sprintf(`{"timestamp":%q}`, now.Add(-backupAge).Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(backupDir, "backup_state.json"), []byte(stateJSON), 0o644); err != nil {
		t.Fatalf("write backup_state.json: %v", err)
	}

	aged := now.Add(-72 * time.Hour)
	seed := []beads.Bead{
		sessionRetentionBead("aged-1", "closed", aged, aged),
		sessionRetentionBead("aged-2", "closed", aged.Add(time.Minute), aged.Add(time.Minute)),
	}
	seed = append(seed, extra...)
	store := beads.NewMemStoreFrom(100, seed, nil)
	var stderrBuf bytes.Buffer
	cr := &CityRuntime{
		cityName:            "test-city",
		cityPath:            cityDir,
		cfg:                 &config.City{Workspace: config.Workspace{Name: "test-city"}},
		standaloneCityStore: store,
		stdout:              io.Discard,
		stderr:              &stderrBuf,
		logPrefix:           "gc test",
	}
	return cr, store, &stderrBuf
}

func mustBeadGone(t *testing.T, store beads.Store, id string) {
	t.Helper()
	if _, err := store.Get(id); !errors.Is(err, beads.ErrNotFound) {
		t.Fatalf("Get(%s) err = %v, want ErrNotFound (aged closed session bead should be pruned)", id, err)
	}
}

func mustBeadPresent(t *testing.T, store beads.Store, id, why string) {
	t.Helper()
	if _, err := store.Get(id); err != nil {
		t.Fatalf("Get(%s) err = %v, want preserved (%s)", id, err, why)
	}
}

func TestSweepClosedSessionBeads_OnlyPrunesAgedClosedSessionBeads(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	aged := now.Add(-72 * time.Hour)
	fresh := now.Add(-1 * time.Hour)
	store := beads.NewMemStoreFrom(100, []beads.Bead{
		sessionRetentionBead("gone-1", "closed", aged, aged),
		sessionRetentionBead("gone-2", "closed", aged.Add(time.Minute), aged.Add(time.Minute)),
		// Closed but inside the TTL — the most recent history stays readable.
		sessionRetentionBead("keep-fresh", "closed", fresh, fresh),
		// Aged but OPEN: an open session bead is a live slot name, a claim, or
		// a resumable incarnation. This is the row that must never be touched.
		sessionRetentionBead("keep-open", "in_progress", aged, aged),
		// Aged and closed, but not the session class: the sessions store is
		// shared with wait gates.
		{
			ID: "keep-wait", Title: "wait gate", Status: "closed",
			Type: sessionpkg.WaitBeadType, Labels: []string{sessionpkg.WaitBeadLabel},
			CreatedAt: aged, UpdatedAt: aged,
		},
		// Aged and closed, but the work class.
		{
			ID: "keep-work", Title: "real work", Status: "closed", Type: "task",
			CreatedAt: aged, UpdatedAt: aged,
		},
	}, nil)

	deleted, err := sweepClosedSessionBeads(store, now, defaultSessionBeadDeleteAfterClose, 10)
	if err != nil {
		t.Fatalf("sweepClosedSessionBeads: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2", deleted)
	}
	mustBeadGone(t, store, "gone-1")
	mustBeadGone(t, store, "gone-2")
	mustBeadPresent(t, store, "keep-fresh", "closed inside delete_after_close")
	mustBeadPresent(t, store, "keep-open", "open session bead holds a slot and a claim")
	mustBeadPresent(t, store, "keep-wait", "wait gate is a different bead policy")
	mustBeadPresent(t, store, "keep-work", "work bead is a different bead policy")
}

func TestSweepClosedSessionBeads_StopsAtDeleteBudget(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	aged := now.Add(-72 * time.Hour)
	seed := make([]beads.Bead, 0, 5)
	for i := range 5 {
		at := aged.Add(time.Duration(i) * time.Minute)
		seed = append(seed, sessionRetentionBead(fmt.Sprintf("seat-%02d", i), "closed", at, at))
	}
	store := beads.NewMemStoreFrom(100, seed, nil)

	deleted, err := sweepClosedSessionBeads(store, now, defaultSessionBeadDeleteAfterClose, 2)
	if err != nil {
		t.Fatalf("sweepClosedSessionBeads: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2 (the per-run budget)", deleted)
	}
	// Oldest-first: the budget spends on the aged head of the queue, and the
	// rest drains on later runs.
	mustBeadGone(t, store, "seat-00")
	mustBeadGone(t, store, "seat-01")
	for i := 2; i < 5; i++ {
		mustBeadPresent(t, store, fmt.Sprintf("seat-%02d", i), "inside the per-run delete budget")
	}
}

func TestSweepClosedSessionBeads_ZeroBudgetAndNilStoreAreNoOps(t *testing.T) {
	now := time.Now()
	store := beads.NewMemStoreFrom(10, []beads.Bead{
		sessionRetentionBead("aged", "closed", now.Add(-72*time.Hour), now.Add(-72*time.Hour)),
	}, nil)
	if n, err := sweepClosedSessionBeads(nil, now, defaultSessionBeadDeleteAfterClose, 10); err != nil || n != 0 {
		t.Fatalf("nil store: n=%d err=%v, want 0 nil", n, err)
	}
	if n, err := sweepClosedSessionBeads(store, now, defaultSessionBeadDeleteAfterClose, 0); err != nil || n != 0 {
		t.Fatalf("zero budget: n=%d err=%v, want 0 nil", n, err)
	}
	mustBeadPresent(t, store, "aged", "a zero budget deletes nothing")
}

func TestSessionBeadRetentionPolicyForConfig(t *testing.T) {
	policyCfg := func(deleteAfterClose string) *config.City {
		return &config.City{Beads: config.BeadsConfig{Policies: map[string]config.BeadPolicyConfig{
			beadPolicySession: {DeleteAfterClose: deleteAfterClose},
		}}}
	}
	tests := []struct {
		name string
		cfg  *config.City
		want time.Duration
	}{
		{"nil config falls back to the shipped default", nil, defaultSessionBeadDeleteAfterClose},
		{"unconfigured falls back to the shipped default", &config.City{}, defaultSessionBeadDeleteAfterClose},
		{"zero ttl falls back to the shipped default", policyCfg("0"), defaultSessionBeadDeleteAfterClose},
		{"configured ttl wins", policyCfg("12h"), 12 * time.Hour},
		{"day units parse", policyCfg("5d"), 120 * time.Hour},
		// A mistyped short ttl must not become a history wipe.
		{"short ttl clamps to the floor", policyCfg("1m"), minSessionBeadDeleteAfterClose},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionBeadRetentionPolicyForConfig(tt.cfg); got != tt.want {
				t.Fatalf("sessionBeadRetentionPolicyForConfig() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRunSessionBeadRetentionWatchdog_PrunesAndLogsCount(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	aged := now.Add(-72 * time.Hour)
	// A fresh backup: the mass delete is allowed behind it.
	cr, store, stderrBuf := seedSessionBeadRetentionCity(t, now, time.Hour,
		sessionRetentionBead("keep-open", "in_progress", aged, aged))

	cr.runSessionBeadRetentionWatchdog(now)

	mustBeadGone(t, store, "aged-1")
	mustBeadGone(t, store, "aged-2")
	mustBeadPresent(t, store, "keep-open", "an open session bead is never retention material")
	if got := stderrBuf.String(); !strings.Contains(got, "pruned 2 closed session bead(s)") {
		t.Fatalf("stderr = %q, want the pruned count", got)
	}
	if !cr.sessionBeadRetentionWatchdogLast.Equal(now) {
		t.Fatalf("sessionBeadRetentionWatchdogLast = %v, want %v", cr.sessionBeadRetentionWatchdogLast, now)
	}
}

func TestRunSessionBeadRetentionWatchdog_SkipsWhenIntervalNotElapsed(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	cr, store, stderrBuf := seedSessionBeadRetentionCity(t, now, time.Hour)
	cr.sessionBeadRetentionWatchdogLast = now.Add(time.Minute)

	cr.runSessionBeadRetentionWatchdog(now)

	mustBeadPresent(t, store, "aged-1", "the interval has not elapsed")
	if got := stderrBuf.String(); got != "" {
		t.Fatalf("stderr = %q, want silence on the skipped path", got)
	}
}

func TestRunSessionBeadRetentionWatchdog_SkipsBulkDeleteWhenBackupStale(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	// 48h since the last backup, past the bulkDeleteMaxAge default: deleting
	// 44k rows of history with a stale recovery point is not worth one TTL.
	cr, store, stderrBuf := seedSessionBeadRetentionCity(t, now, 48*time.Hour)

	cr.runSessionBeadRetentionWatchdog(now)

	mustBeadPresent(t, store, "aged-1", "the backup is stale")
	mustBeadPresent(t, store, "aged-2", "the backup is stale")
	if got := stderrBuf.String(); !strings.Contains(got, "skipping bulk delete") {
		t.Fatalf("stderr = %q, want 'skipping bulk delete'", got)
	}
	// The interval stamp is consumed even on the skip path, so a blocked
	// watchdog does not re-scan every tick.
	if !cr.sessionBeadRetentionWatchdogLast.Equal(now) {
		t.Fatalf("sessionBeadRetentionWatchdogLast = %v, want %v", cr.sessionBeadRetentionWatchdogLast, now)
	}
}

func TestRunSessionBeadRetentionWatchdog_NilCfgSkipsWithoutPanic(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	cr, store, _ := seedSessionBeadRetentionCity(t, now, time.Hour)
	cr.cfg = nil

	cr.runSessionBeadRetentionWatchdog(now)

	mustBeadPresent(t, store, "aged-1", "no config means no declared intent to delete history")
}
