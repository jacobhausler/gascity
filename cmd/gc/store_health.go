package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/storehealth"
)

// statusStoreHealthTimeout bounds the store-health row count so a live city
// with a large closed-history table cannot stall `gc status` for minutes. The
// count drives only the on-disk size ratio and is best-effort, so a timeout
// returns 0 — mirroring the API server's countBeadStoreRows defense
// (internal/api/store_health.go, statusStoreReadTimeout), which this CLI local
// fallback never inherited. It matches the server's 1s bound.
const statusStoreHealthTimeout = time.Second

// storeHealthFromInputs assembles a CLI-facing *StoreHealth from the raw
// measurements. LastGCAt is serialized as RFC3339 UTC when present;
// when the maintenance log is empty, LastGCAt and LastGCStatus are
// omitted (json:"omitempty").
func storeHealthFromInputs(cityPath string, sizeBytes int64, liveRows int, lastGCAt time.Time, lastGCStatus string) *StoreHealth {
	h := storehealth.Compute(cityPath, sizeBytes, liveRows, lastGCAt, lastGCStatus)
	out := &StoreHealth{
		Path:        h.Path,
		SizeBytes:   h.SizeBytes,
		LiveRows:    h.LiveRows,
		RatioMB:     h.RatioMB,
		Warning:     h.Warning,
		ThresholdMB: h.ThresholdMB,
	}
	switch {
	case !h.LastGCAt.IsZero():
		out.LastGCAt = h.LastGCAt.UTC().Format(time.RFC3339)
		out.LastGCStatus = h.LastGCStatus
	case h.LastGCStatus != "":
		// No timestamp, but a non-empty status ("unknown" when the bounded
		// scan could not complete in time) — surface it distinctly from
		// "" (scan completed, no maintenance event found). Dropping this
		// silently would report "never" when the truth is "don't know".
		out.LastGCStatus = h.LastGCStatus
	}
	return out
}

// statusStoreHealthMaintenanceTimeout bounds storehealth.LastMaintenance so a
// live city with a large events.jsonl cannot stall `gc status` for seconds.
// LastMaintenance already fast-paths via events.TailProvider when available
// (ra-ahlsc), but its rare fallback (no matching event ever seen in the
// active file — a legitimate "never happened", or an occurrence rotated into
// an archive) is still an unbounded scan; this timeout is the backstop for
// that case, mirroring statusStoreHealthTimeout above. On timeout the result
// is reported as "unknown" — distinct from "" (scan completed, genuinely
// none found) — because a timeout means the scan did NOT complete and we
// cannot honestly claim maintenance never ran.
const statusStoreHealthMaintenanceTimeout = time.Second

// lastMaintenanceUnknownStatus marks a LastMaintenance call that could not
// complete within statusStoreHealthMaintenanceTimeout. It is deliberately
// distinct from the empty string, which means the scan completed and found
// no store-maintenance event.
const lastMaintenanceUnknownStatus = "unknown"

// collectStoreHealth measures the Dolt store at cityPath and the latest
// maintenance event via ep, returning a populated *StoreHealth.
// liveRowCount provides the live row count; callers without a store pass
// nil and LiveRows is reported as zero.
func collectStoreHealth(cityPath string, store beads.Store, ep events.Provider) *StoreHealth {
	size := storehealth.WalkSize(storehealth.StorePath(cityPath))
	rows := liveRowCount(store)
	lastAt, lastStatus := lastMaintenanceWithTimeout(ep)
	return storeHealthFromInputs(cityPath, size, rows, lastAt, lastStatus)
}

// lastMaintenanceWithTimeout runs storehealth.LastMaintenance on a goroutine
// and returns its result, or (zero, "unknown") if it does not finish within
// statusStoreHealthMaintenanceTimeout. storehealth.LastMaintenance takes no
// context, so a stalled scan cannot be canceled — the goroutine is left to
// finish on its own (harmless in the short-lived `gc status` process).
// Mirrors listBeadsWithTimeout below.
func lastMaintenanceWithTimeout(ep events.Provider) (time.Time, string) {
	if ep == nil {
		return time.Time{}, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), statusStoreHealthMaintenanceTimeout)
	defer cancel()
	type maintResult struct {
		ts     time.Time
		status string
	}
	done := make(chan maintResult, 1)
	go func() {
		ts, status := storehealth.LastMaintenance(ep)
		done <- maintResult{ts: ts, status: status}
	}()
	select {
	case r := <-done:
		return r.ts, r.status
	case <-ctx.Done():
		return time.Time{}, lastMaintenanceUnknownStatus
	}
}

// liveRowCount returns the number of beads known to store, or 0 when store is
// nil, the count fails, or it does not finish within statusStoreHealthTimeout.
// Counts all statuses (including closed) because the ratio is about on-disk row
// footprint, not actionable work — but that closed-inclusive scan is never
// cache-answerable and hydrates the whole history from the backend, so it is
// bounded to keep `gc status` responsive. A Counter-capable store (Dolt /
// CachingStore) answers from the catalog without hydrating rows; otherwise a
// bounded full scan is the fallback.
func liveRowCount(store beads.Store) int {
	if store == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), statusStoreHealthTimeout)
	defer cancel()
	query := beads.ListQuery{AllowScan: true, IncludeClosed: true}
	if counter, ok := store.(beads.Counter); ok {
		if n, err := counter.Count(ctx, query); err == nil {
			return n
		}
	}
	list, err := listBeadsWithTimeout(ctx, store, query)
	if err != nil {
		return 0
	}
	return len(list)
}

// listBeadsWithTimeout runs store.List on a goroutine and returns its result,
// or ctx.Err() if the deadline fires first. beads.Store.List takes no context,
// so a stalled scan cannot be canceled — the goroutine is left to finish on
// its own (harmless in the short-lived `gc status` process). Mirrors the API
// server's statusListStoreWithTimeout.
func listBeadsWithTimeout(ctx context.Context, store beads.Store, query beads.ListQuery) ([]beads.Bead, error) {
	type listResult struct {
		list []beads.Bead
		err  error
	}
	done := make(chan listResult, 1)
	go func() {
		list, err := store.List(query)
		done <- listResult{list: list, err: err}
	}()
	select {
	case r := <-done:
		return r.list, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// renderStoreHealthBlock prints the human-readable "Store health:"
// block that follows the summary of gc status. No-op when h is nil.
func renderStoreHealthBlock(w io.Writer, h *StoreHealth) {
	if h == nil {
		return
	}
	fmt.Fprintln(w)                                                        //nolint:errcheck // best-effort stdout
	fmt.Fprintln(w, "Store health:")                                       //nolint:errcheck // best-effort stdout
	fmt.Fprintf(w, "  Path:        %s\n", h.Path)                          //nolint:errcheck // best-effort stdout
	fmt.Fprintf(w, "  Size:        %s\n", storeHealthSIBytes(h.SizeBytes)) //nolint:errcheck // best-effort stdout
	fmt.Fprintf(w, "  Live rows:   %d\n", h.LiveRows)                      //nolint:errcheck // best-effort stdout
	suffix := ""
	if h.Warning {
		suffix = "  \u26a0 maintenance overdue"
	}
	fmt.Fprintf(w, "  Ratio:       %.1f MB/row  (threshold %.1f MB/row)%s\n", h.RatioMB, h.ThresholdMB, suffix) //nolint:errcheck // best-effort stdout
	switch {
	case h.LastGCAt != "":
		fmt.Fprintf(w, "  Last GC:     %s (%s)\n", h.LastGCAt, h.LastGCStatus) //nolint:errcheck // best-effort stdout
	case h.LastGCStatus != "":
		fmt.Fprintf(w, "  Last GC:     %s\n", h.LastGCStatus) //nolint:errcheck // best-effort stdout
	}
}

// storeHealthSIBytes formats n with SI prefixes (1 KB = 1000 B, 1 MB =
// 10^6 B, 1 GB = 10^9 B) to match the MB-per-row ratio (which is SI).
func storeHealthSIBytes(n int64) string {
	const (
		kb = int64(1_000)
		mb = int64(1_000_000)
		gb = int64(1_000_000_000)
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
