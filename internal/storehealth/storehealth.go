// Package storehealth computes the Dolt bead store health summary used
// by gc status and the /v0/status API. The summary is: store path on
// disk, raw size in bytes, the retained row count of the city store
// (including open and closed beads), a derived MB-per-row ratio, and a
// warning flag when the ratio exceeds the configured threshold.
//
// Design: ADR 0002 (docs/adr/0002-dolt-store-maintenance-runbook.md)
// and bead ga-d5y design D9.
package storehealth

import (
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/fsys"
)

// DefaultThresholdMB is the MB-per-row threshold above which maintenance
// is flagged overdue. 1 MB per row matches the bad case observed in
// production (.beads/dolt at ~11 GB with ~64 rows).
const DefaultThresholdMB = 1.0

// Health summarizes disk and maintenance health of the Dolt bead store.
// A pointer *Health is included in status payloads so "no data" (e.g.
// supervisor not running) is representable as nil rather than a
// confusing zero-valued block.
type Health struct {
	Path         string
	SizeBytes    int64
	LiveRows     int
	RatioMB      float64
	Warning      bool
	ThresholdMB  float64
	LastGCAt     time.Time
	LastGCStatus string
}

// StorePath returns the canonical on-disk location of the Dolt store
// for a city rooted at cityPath.
func StorePath(cityPath string) string {
	metaPath := filepath.Join(cityPath, ".beads", "metadata.json")
	if state, ok, err := contract.LoadMetadataState(fsys.OSFS{}, metaPath); err == nil && ok {
		if strings.EqualFold(strings.TrimSpace(state.Backend), "doltlite") {
			return filepath.Join(cityPath, ".beads", "doltlite")
		}
	}
	return filepath.Join(cityPath, ".beads", "dolt")
}

// Compute builds a Health from measured inputs. Pure function — all
// I/O is performed by the caller via WalkSize and LastMaintenance.
func Compute(cityPath string, sizeBytes int64, retainedRows int, lastGCAt time.Time, lastGCStatus string) Health {
	h := Health{
		Path:         StorePath(cityPath),
		SizeBytes:    sizeBytes,
		LiveRows:     retainedRows,
		ThresholdMB:  DefaultThresholdMB,
		LastGCAt:     lastGCAt,
		LastGCStatus: lastGCStatus,
	}
	if retainedRows > 0 {
		h.RatioMB = float64(sizeBytes) / (bytesPerMB * float64(retainedRows))
		h.Warning = sizeBytes > int64(DefaultThresholdMB*bytesPerMB)*int64(retainedRows)
	}
	return h
}

// WalkSize returns the total size in bytes of path's contents,
// recursing into subdirectories. Missing paths and read errors are
// treated as zero bytes — a fresh city has no Dolt directory yet, and
// partial read failures during maintenance should not mask the rest
// of the status output.
func WalkSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}

// LastMaintenance returns the timestamp and status ("success" or
// "failed") of the most-recent store-maintenance event in provider.
// Zero time and empty status when no events, provider is nil, or the
// provider returns an error.
//
// Shape: a naive implementation does two unbounded full-file List scans
// (one per event type) — on a large, long-lived events.jsonl this alone
// was measured to cost 5-10s, the entire floor of `gc status` (ra-21k1t).
// When ep also implements events.TailProvider, LastMaintenance instead
// asks for just the single trailing matching event of each type. A tail
// hit is always authoritative: archives are strictly older than the
// active file a TailProvider reads, so a match found there can never be
// beaten by anything rotated out of it. Only when NEITHER type has a
// tail hit — genuinely never happened, or its only occurrence rotated
// into an archive the tail read does not cover — does this fall back to
// the full (archive-inclusive) scan, so the answer is never wrong, only
// occasionally as slow as before. Callers that cannot afford that rare
// slow path should bound it themselves (see cmd/gc's
// storeHealthMaintenanceTimeout, which mirrors this pattern).
func LastMaintenance(ep events.Provider) (time.Time, string) {
	if ep == nil {
		return time.Time{}, ""
	}
	if tp, ok := ep.(events.TailProvider); ok {
		if ts, status, found := latestMaintenanceEvent(func(f events.Filter) ([]events.Event, error) {
			return tp.ListTail(f, 1)
		}); found {
			return ts, status
		}
	}
	ts, status, _ := latestMaintenanceEvent(ep.List)
	return ts, status
}

// latestMaintenanceEvent finds the most-recent store-maintenance event
// across both StoreMaintenanceDone and StoreMaintenanceFailed by calling
// list once per type. found is false only when list returned no matching
// event (and no error) for either type — i.e. a genuine, complete answer
// of "none seen by this list call", as opposed to a list error, which is
// silently skipped exactly as the pre-existing behavior did.
func latestMaintenanceEvent(list func(events.Filter) ([]events.Event, error)) (time.Time, string, bool) {
	var (
		latestTs     time.Time
		latestStatus string
		found        bool
	)
	for _, spec := range []struct {
		typ    string
		status string
	}{
		{events.StoreMaintenanceDone, "success"},
		{events.StoreMaintenanceFailed, "failed"},
	} {
		evts, err := list(events.Filter{Type: spec.typ})
		if err != nil {
			continue
		}
		for _, e := range evts {
			if !found || e.Ts.After(latestTs) {
				latestTs = e.Ts
				latestStatus = spec.status
				found = true
			}
		}
	}
	return latestTs, latestStatus, found
}

const bytesPerMB = 1_000_000
