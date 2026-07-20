package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// fakeHealthStore is a beads.Store whose List and Count behavior the test
// controls. The embedded nil interface is never used — liveRowCount only calls
// Count (via the beads.Counter assertion) and List.
type fakeHealthStore struct {
	beads.Store
	countFn func(context.Context, beads.ListQuery) (int, error)
	listFn  func(beads.ListQuery) ([]beads.Bead, error)
}

func (f *fakeHealthStore) Count(ctx context.Context, q beads.ListQuery, _ ...string) (int, error) {
	return f.countFn(ctx, q)
}

func (f *fakeHealthStore) List(q beads.ListQuery) ([]beads.Bead, error) {
	return f.listFn(q)
}

// TestLiveRowCountBoundsSlowScan is the regression for the ~105s silent stall in
// `gc status`: liveRowCount ran an unbounded IncludeClosed full-history scan
// (store.List) with no timeout, so a live city with a large closed-history
// table hung status for ~2 minutes. When the Counter cannot answer, the scan
// must be bounded and return 0 (best-effort) rather than stall.
func TestLiveRowCountBoundsSlowScan(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) }) // let the leaked List goroutine exit
	store := &fakeHealthStore{
		countFn: func(context.Context, beads.ListQuery) (int, error) {
			return 0, errors.New("count unsupported for this query")
		},
		listFn: func(beads.ListQuery) ([]beads.Bead, error) {
			<-release // simulate the multi-minute closed-history hydration
			return nil, nil
		},
	}

	start := time.Now()
	got, complete := liveRowCount(store)
	elapsed := time.Since(start)

	if got != 0 {
		t.Fatalf("liveRowCount = %d, want 0 when the scan times out", got)
	}
	if complete {
		t.Fatalf("complete = true, want false when the scan times out — a timeout zero must be flagged incomplete (ra-nka1c)")
	}
	if elapsed > statusStoreHealthTimeout+2*time.Second {
		t.Fatalf("liveRowCount did not bound the scan: took %s (bound %s)", elapsed, statusStoreHealthTimeout)
	}
}

// TestLiveRowCountUsesCounterFastPath pins that a Counter-capable store answers
// from the catalog without hydrating rows — List must not be called.
func TestLiveRowCountUsesCounterFastPath(t *testing.T) {
	store := &fakeHealthStore{
		countFn: func(_ context.Context, q beads.ListQuery) (int, error) {
			if !q.IncludeClosed {
				t.Errorf("row-footprint count must IncludeClosed, got query %+v", q)
			}
			return 42, nil
		},
		listFn: func(beads.ListQuery) ([]beads.Bead, error) {
			t.Fatal("List must not be called when the Counter answers")
			return nil, nil
		},
	}

	got, complete := liveRowCount(store)
	if got != 42 {
		t.Fatalf("liveRowCount = %d, want 42 from the Counter fast path", got)
	}
	if !complete {
		t.Fatalf("complete = false, want true when the Counter answers")
	}
}

// TestLiveRowCountFabricatedZeroWouldPassWarningCheck is the falsifiable
// control for the incomplete-scan case above: it proves an incomplete zero
// row count, left unflagged, silently reads as "healthy" through
// storeHealthFromInputs — the exact failure this fix closes. It must FAIL on
// the pre-fix behavior (complete ignored, Warning computed from the raw
// zero) and PASS once Warning is forced true for an incomplete count.
func TestLiveRowCountFabricatedZeroWouldPassWarningCheck(t *testing.T) {
	h := storeHealthFromInputs("/c", 410_781_063, 0, false, time.Time{}, "")
	if !h.Warning {
		t.Fatalf("Warning = false for an incomplete 0-row count against a 410MB store; want true — an unflagged incomplete count reads as healthy")
	}
	if h.LiveRowsComplete {
		t.Fatalf("LiveRowsComplete = true, want false")
	}
}
