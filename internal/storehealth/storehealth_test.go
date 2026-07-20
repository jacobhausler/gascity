package storehealth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/events"
)

func TestStorePath(t *testing.T) {
	got := StorePath("/tmp/citysvc")
	want := filepath.Join("/tmp/citysvc", ".beads", "dolt")
	if got != want {
		t.Fatalf("StorePath = %q, want %q", got, want)
	}
}

func TestStorePath_DoltliteMetadata(t *testing.T) {
	cityPath := t.TempDir()
	beadsDir := filepath.Join(cityPath, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"doltlite","database":"doltlite","dolt_database":"hq"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := StorePath(cityPath)
	want := filepath.Join(cityPath, ".beads", "doltlite")
	if got != want {
		t.Fatalf("StorePath = %q, want %q", got, want)
	}
}

func TestComputeWarningHighRatio(t *testing.T) {
	// 11.2 GB (decimal) / 221 rows = ~50.68 MB/row, warning.
	const size = 11_200_000_000
	h := Compute("/c", size, 221, time.Time{}, "")
	if !h.Warning {
		t.Fatalf("Warning = false, want true for size=%d rows=221", size)
	}
	if h.RatioMB < 50 || h.RatioMB > 51 {
		t.Fatalf("RatioMB = %v, want ~50.7", h.RatioMB)
	}
	if h.ThresholdMB != DefaultThresholdMB {
		t.Fatalf("ThresholdMB = %v, want %v", h.ThresholdMB, DefaultThresholdMB)
	}
	if h.Path != "/c/.beads/dolt" {
		t.Fatalf("Path = %q, want /c/.beads/dolt", h.Path)
	}
}

func TestComputeNoWarningLowRatio(t *testing.T) {
	// 50 MB / 221 rows = ~0.23 MB/row, no warning.
	const size = 50_000_000
	h := Compute("/c", size, 221, time.Time{}, "")
	if h.Warning {
		t.Fatalf("Warning = true, want false for size=%d rows=221", size)
	}
	if h.RatioMB > 0.5 {
		t.Fatalf("RatioMB = %v, want < 0.5", h.RatioMB)
	}
}

func TestComputeZeroRetainedRowsDoesNotWarnForBookkeepingBytes(t *testing.T) {
	// The denominator is retained rows (open and closed). A genuinely empty
	// store can still contain bookkeeping files, which alone are not unhealthy.
	h := Compute("/c", 1, 0, time.Time{}, "")
	if h.Warning {
		t.Fatalf("Warning = true, want false for bookkeeping bytes with zero retained rows")
	}
}

func TestComputeZeroEverything(t *testing.T) {
	h := Compute("/c", 0, 0, time.Time{}, "")
	if h.Warning {
		t.Fatalf("Warning = true, want false for all-zero inputs")
	}
}

func TestComputeBoundary(t *testing.T) {
	// Exactly at the threshold: size = 1M * rows should NOT warn
	// (the inequality is strict ">", not ">=").
	const rows = 10
	h := Compute("/c", int64(DefaultThresholdMB*bytesPerMB)*int64(rows), rows, time.Time{}, "")
	if h.Warning {
		t.Fatalf("Warning = true at exact threshold, want false")
	}
	h = Compute("/c", int64(DefaultThresholdMB*bytesPerMB)*int64(rows)+1, rows, time.Time{}, "")
	if !h.Warning {
		t.Fatalf("Warning = false one byte over threshold, want true")
	}
}

func TestComputeCarriesLastGC(t *testing.T) {
	ts := time.Date(2026, 4, 1, 3, 0, 0, 0, time.UTC)
	h := Compute("/c", 1, 1, ts, "success")
	if !h.LastGCAt.Equal(ts) {
		t.Fatalf("LastGCAt = %v, want %v", h.LastGCAt, ts)
	}
	if h.LastGCStatus != "success" {
		t.Fatalf("LastGCStatus = %q, want success", h.LastGCStatus)
	}
}

func TestWalkSizeMissingPath(t *testing.T) {
	got := WalkSize(filepath.Join(t.TempDir(), "nonexistent"))
	if got != 0 {
		t.Fatalf("WalkSize(missing) = %d, want 0", got)
	}
}

func TestWalkSizeSumsFiles(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel string, size int) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	mustWrite("a.bin", 100)
	mustWrite("sub/b.bin", 250)
	mustWrite("sub/deeper/c.bin", 17)
	got := WalkSize(dir)
	if got != 367 {
		t.Fatalf("WalkSize = %d, want 367", got)
	}
}

func TestLastMaintenanceNilProvider(t *testing.T) {
	ts, status := LastMaintenance(nil)
	if !ts.IsZero() || status != "" {
		t.Fatalf("LastMaintenance(nil) = (%v,%q), want (zero,\"\")", ts, status)
	}
}

func TestLastMaintenanceReturnsLatestAcrossTypes(t *testing.T) {
	ep := events.NewFake()
	older := time.Date(2026, 4, 1, 3, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 4, 8, 3, 0, 0, 0, time.UTC)

	payloadDone, _ := json.Marshal(events.StoreMaintenanceDonePayload{DurationSeconds: 1})
	payloadFail, _ := json.Marshal(events.StoreMaintenanceFailedPayload{Stage: "gc"})

	ep.Record(events.Event{Type: events.StoreMaintenanceDone, Ts: older, Payload: payloadDone})
	ep.Record(events.Event{Type: events.StoreMaintenanceFailed, Ts: newer, Payload: payloadFail})

	ts, status := LastMaintenance(ep)
	if !ts.Equal(newer) {
		t.Fatalf("ts = %v, want %v", ts, newer)
	}
	if status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
}

func TestLastMaintenanceOnlyDoneEvents(t *testing.T) {
	ep := events.NewFake()
	t1 := time.Date(2026, 4, 1, 3, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 8, 3, 0, 0, 0, time.UTC)
	payload, _ := json.Marshal(events.StoreMaintenanceDonePayload{DurationSeconds: 2})
	ep.Record(events.Event{Type: events.StoreMaintenanceDone, Ts: t1, Payload: payload})
	ep.Record(events.Event{Type: events.StoreMaintenanceDone, Ts: t2, Payload: payload})

	ts, status := LastMaintenance(ep)
	if !ts.Equal(t2) {
		t.Fatalf("ts = %v, want %v", ts, t2)
	}
	if status != "success" {
		t.Fatalf("status = %q, want success", status)
	}
}

func TestLastMaintenanceNoEvents(t *testing.T) {
	ep := events.NewFake()
	ts, status := LastMaintenance(ep)
	if !ts.IsZero() || status != "" {
		t.Fatalf("LastMaintenance(empty) = (%v,%q), want (zero,\"\")", ts, status)
	}
}

// listOnlyProvider implements events.Provider but deliberately NOT
// events.TailProvider, so LastMaintenance must take the plain-List
// fallback path rather than crashing or silently returning nothing.
type listOnlyProvider struct {
	events.Provider
	listCalls int
}

func (p *listOnlyProvider) List(filter events.Filter) ([]events.Event, error) {
	p.listCalls++
	return p.Provider.List(filter)
}

func TestLastMaintenanceFallsBackWithoutTailProvider(t *testing.T) {
	fake := events.NewFake()
	ts1 := time.Date(2026, 4, 1, 3, 0, 0, 0, time.UTC)
	payload, _ := json.Marshal(events.StoreMaintenanceDonePayload{DurationSeconds: 1})
	fake.Record(events.Event{Type: events.StoreMaintenanceDone, Ts: ts1, Payload: payload})

	p := &listOnlyProvider{Provider: fake}
	// Sanity: listOnlyProvider itself does not implement TailProvider.
	if _, ok := interface{}(p).(events.TailProvider); ok {
		t.Fatalf("listOnlyProvider unexpectedly implements TailProvider")
	}

	ts, status := LastMaintenance(p)
	if !ts.Equal(ts1) || status != "success" {
		t.Fatalf("LastMaintenance(listOnly) = (%v,%q), want (%v,success)", ts, status, ts1)
	}
	if p.listCalls != 2 {
		t.Fatalf("List called %d times, want 2 (one per event type, fallback path)", p.listCalls)
	}
}

// tailStubProvider lets a test control exactly what ListTail vs. List
// return, independent of each other — modeling a provider (like
// FileRecorder) whose ListTail only covers the active file while List
// also covers older, archived events.
type tailStubProvider struct {
	events.Provider
	tail map[string][]events.Event // by filter.Type
}

func (p *tailStubProvider) ListTail(filter events.Filter, limit int) ([]events.Event, error) {
	evts := p.tail[filter.Type]
	if len(evts) > limit {
		evts = evts[len(evts)-limit:]
	}
	return evts, nil
}

func TestLastMaintenanceFallsBackWhenTailMissesAnArchivedEvent(t *testing.T) {
	fake := events.NewFake()
	archived := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	payload, _ := json.Marshal(events.StoreMaintenanceDonePayload{DurationSeconds: 1})
	// The only maintenance event ever recorded is old enough that it has
	// rotated out of the active file — ListTail (active-file-only) will
	// not see it, but the full List (which also covers archives) will.
	fake.Record(events.Event{Type: events.StoreMaintenanceDone, Ts: archived, Payload: payload})

	p := &tailStubProvider{Provider: fake, tail: map[string][]events.Event{}}

	ts, status := LastMaintenance(p)
	if !ts.Equal(archived) || status != "success" {
		t.Fatalf("LastMaintenance(tail-miss) = (%v,%q), want (%v,success) via fallback", ts, status, archived)
	}
}

func TestLastMaintenanceTailHitSkipsFallback(t *testing.T) {
	recent := time.Date(2026, 7, 19, 20, 0, 0, 0, time.UTC)
	payload, _ := json.Marshal(events.StoreMaintenanceDonePayload{DurationSeconds: 1})
	recentEvt := events.Event{Type: events.StoreMaintenanceDone, Ts: recent, Payload: payload}

	underlying := &listOnlyProvider{Provider: events.NewFake()}
	p := &tailStubProvider{
		Provider: underlying,
		tail: map[string][]events.Event{
			events.StoreMaintenanceDone: {recentEvt},
		},
	}

	ts, status := LastMaintenance(p)
	if !ts.Equal(recent) || status != "success" {
		t.Fatalf("LastMaintenance(tail-hit) = (%v,%q), want (%v,success)", ts, status, recent)
	}
	if underlying.listCalls != 0 {
		t.Fatalf("List called %d times, want 0 — a tail hit must skip the full-scan fallback entirely", underlying.listCalls)
	}
}
