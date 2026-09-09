package main

import (
	"fmt"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/coordclass"
	"github.com/gastownhall/gascity/internal/nudgequeue"
)

const (
	nudgeBeadType = "chore"
	// nudgeBeadLabel is the label applied to queued-nudge beads. coordclass
	// mirrors this string privately (as labelNudge) for store routing; the two
	// must stay in sync.
	nudgeBeadLabel = "gc:nudge"
)

type nudgeReference = nudgequeue.Reference

// nudgeBeadStoreHandle pairs the nudges-class store a call may use with the one
// fact its closer needs: whether this call may release it.
//
// Those are not the same question. A city that keeps every class on the reserved
// work binding is served by the store the open below created, and the frame that
// opened it owns that store's release. A city that RELOCATES the nudges class is
// served instead by the store the one-shot funnel memoizes for the whole process
// (cliStorageRoutesByCity), which belongs to that memo and is released only
// where the process ends (closeCLIStorageRoutes). Closing it from a frame
// detaches nothing from the memo, so every later caller in the process — the
// supervisor's own nudge dispatch tick among them — reads through a closed
// handle for the remaining life of the process, and the nudge path stops
// delivering while still reporting a plain "sqlite store: bead store closed".
type nudgeBeadStoreHandle struct {
	store beads.NudgesStore
	// closable is true only when the store above is the one this open created.
	closable bool
}

// openNudgeBeadStoreHandle is THE store seam of the nudge path (mirrors the
// injectable vars in cmd_nudge.go): every production open of a nudges store —
// and so every closer of one — arrives through this variable, so a test can
// substitute a fake and assert both halves of the rule: a per-tick poll helper
// releases every store it opens, and no helper releases the shared binding
// store. Tests that replace this package variable must stay serial; do not use
// t.Parallel in those tests.
var openNudgeBeadStoreHandle = func(cityPath string) nudgeBeadStoreHandle {
	handle, _ := openNudgeBeadStoreHandleErr(cityPath)
	return handle
}

// openNudgeBeadStore is the store-only spelling of the seam, for the helpers
// that use a store and never release it, and the fixture the tests reach for.
// The ownership answer lives on the handle, which is what a closer consumes.
func openNudgeBeadStore(cityPath string) beads.NudgesStore {
	return openNudgeBeadStoreHandle(cityPath).store
}

// openNudgeBeadStoreErr is openNudgeBeadStore with the open failure kept instead
// of swallowed into a nil-safe zero store.
//
// The zero store is not harmless: every nudge helper below is nil-tolerant, so a
// city whose store will not open reported "opening city store for X" with no
// cause at all — the operator could not tell a missing city from a locked
// database from a storage refusal. Call sites that surface a failure to a human
// use this form and print the reason; the seam above stays for the poll/drain
// helpers whose contract is already "a nil store means do nothing".
//
// A call site that CLOSES the store takes openNudgeBeadStoreHandleErr instead:
// the store on its own cannot say whether closing it belongs to that call.
func openNudgeBeadStoreErr(cityPath string) (beads.NudgesStore, error) {
	handle, err := openNudgeBeadStoreHandleErr(cityPath)
	return handle.store, err
}

// openNudgeBeadStoreHandleErr opens the nudges store and states who may close
// it. See nudgeBeadStoreHandle for why the two answers differ.
func openNudgeBeadStoreHandleErr(cityPath string) (nudgeBeadStoreHandle, error) {
	opened, err := openStoreAtForCity(cityPath, cityPath)
	if err != nil {
		return nudgeBeadStoreHandle{}, fmt.Errorf("opening the city store at %q: %w", cityPath, err)
	}
	routes := cliStorageRoutes(cityPath)
	if binding, relocated := routes.storeFor(coordclass.ClassNudges); relocated && binding != nil {
		// The class is served from the binding, so the work store opened a
		// moment above answers nothing for this call. Release it here rather
		// than leak a handle and its read pool on every nudge open; a failed
		// release is a leak, not a reason to refuse an open that worked, so it
		// is not allowed to become this function's error.
		_ = closeBeadStoreHandle(opened) //nolint:errcheck // a leak beats a refusal of a good open
		return nudgeBeadStoreHandle{store: beads.NudgesStore{Store: binding}}, nil
	}
	return nudgeBeadStoreHandle{store: beads.NudgesStore{Store: opened}, closable: true}, nil
}

// nudgeFrontDoor wraps a strongly-typed nudges store as the nudge object's
// front door (internal/nudgequeue.Store). The bead is a SHADOW of the flock'd
// state.json queue; the front door confines the Item<->Bead codec, leaving these
// cmd/gc helpers as thin adapters that keep the methods callable inside the
// withNudgeQueueState transaction.
func nudgeFrontDoor(store beads.NudgesStore) *nudgequeue.Store {
	return nudgequeue.NewStore(store)
}

func ensureQueuedNudgeBead(store beads.NudgesStore, item queuedNudge) (string, bool, error) {
	return nudgeFrontDoor(store).Save(item)
}

func markQueuedNudgeTerminal(store beads.NudgesStore, item queuedNudge, state, reason, commitBoundary string, now time.Time) error {
	return nudgeFrontDoor(store).Terminalize(item, state, reason, commitBoundary, now)
}

func formatOptionalTime(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339)
}
