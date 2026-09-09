package main

import (
	"errors"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/coordclass"
)

// The nudge path's per-call store helpers document themselves as closing "the
// store this frame opened (if any)" — and on a city that keeps every class on
// the reserved work binding, that is exactly what happens.
//
// A city that RELOCATES the nudges class breaks the premise rather than the
// code: openNudgeBeadStoreErr resolves through the process-memoized one-shot
// funnel, so what the frame holds is the binding's own store, which belongs to
// cliStorageRoutesByCity and is released only where the process ends. Closing
// it detaches nothing from the memo, so every later caller in the process —
// including the supervisor's own nudge dispatch tick — reads through a closed
// handle for the remaining life of the process and reports
// "sqlite store: bead store closed". Nudges stop being delivered at that point
// and never resume.
//
// cr-ypk5s: 1583 occurrences of that line in one supervisor.log, one per tick
// with a non-empty queue, on a city whose five infrastructure classes sit on
// one sqlite-beads binding.

// TestNudgeMaintenanceStoreDoesNotCloseTheSharedClassStore pins the ownership
// rule the closer's doc claims: a nudge helper may close a store it opened, and
// must not close the store the process memo serves.
func TestNudgeMaintenanceStoreDoesNotCloseTheSharedClassStore(t *testing.T) {
	cityPath, _ := migratedOneShotCLICity(t)
	captureCLIStorageStderr(t)

	routes := cliStorageRoutes(cityPath)
	if routes == nil {
		t.Fatal("the converged fixture city resolved no one-shot routes")
	}
	shared, relocated := routes.storeFor(coordclass.ClassNudges)
	if !relocated || shared == nil {
		t.Fatal("the fixture city does not relocate the nudges class")
	}

	maint := nudgeMaintenanceStore{cityPath: cityPath}
	opened := maint.ensureOpen()
	if opened.Store != shared {
		t.Fatalf("the maintenance frame holds %T, want the binding store %T it must not own",
			opened.Store, shared)
	}
	if err := maint.close(); err != nil {
		t.Fatalf("closing the maintenance frame: %v", err)
	}

	// The memo still names this store, so this is the read the next caller in
	// the process performs.
	if _, err := shared.Get("cr-store-liveness-probe"); errors.Is(err, beads.ErrStoreClosed) {
		t.Errorf("the maintenance frame closed the process-memoized nudges-class store; "+
			"every later caller in this process now reads a closed handle: %v", err)
	}
	if again := openNudgeBeadStore(cityPath); again.Store != shared {
		t.Errorf("the memo handed the next caller a different store (%T) than the binding's (%T)",
			again.Store, shared)
	}
}
