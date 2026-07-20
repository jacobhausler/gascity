package storehealth

// Manual timing harness for ra-ahlsc, not part of the regular suite.
// Run with:
//   GC_TIMING_FIXTURE=/path/to/events.jsonl go test ./internal/storehealth/ -run TestManualLastMaintenanceTiming -v
// Deliberately skipped unless GC_TIMING_FIXTURE is set, since it depends on
// a large external fixture that must never be the live, concurrently-written
// ~/randland/.gc/events.jsonl (copy it to scratch first).

import (
	"io"
	"os"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/events"
)

func TestManualLastMaintenanceTiming(t *testing.T) {
	fixture := os.Getenv("GC_TIMING_FIXTURE")
	if fixture == "" {
		t.Skip("GC_TIMING_FIXTURE not set")
	}
	rec, err := events.NewFileRecorder(fixture, io.Discard)
	if err != nil {
		t.Fatalf("NewFileRecorder: %v", err)
	}
	defer rec.Close() //nolint:errcheck

	start := time.Now()
	ts, status := LastMaintenance(rec)
	elapsed := time.Since(start)
	t.Logf("LastMaintenance over %s: ts=%v status=%q elapsed=%v", fixture, ts, status, elapsed)
}
