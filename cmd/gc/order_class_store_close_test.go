package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/coordclass"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/orderdispatch"
	"github.com/gastownhall/gascity/internal/orders"
)

// This file sweeps the "a frame closes a store it did not open" predicate across
// the ORDER lane — the sibling sweep cr-ypk5s asked for (cr-tzzvo) after the
// nudge lane was found closing the process-memoized class store.
//
// On a city that relocates the ORDERS class (westlands city.toml puts graph,
// sessions, messaging, orders and nudges on one sqlite-beads "infra" binding),
// resolveClassStore hands out ONE shared handle: routes.storeFor
// (cmd/gc/class_store.go) is the only producer of a process-shared class store,
// and the handle belongs to whoever built the routes — the controller's boot, or
// the one-shot CLI memo that only closeCLIStorageRoutes() detaches. A leaf that
// closes it detaches nothing from that owner, so every later caller in the
// process reads a closed handle until the process ends. That is the nudge
// failure (1583 "sqlite store: bead store closed" lines in one supervisor.log),
// and the same sentence reaching the orders lane is why this sweep exists:
// "gc: order nightly-light-audit: closing tracking bead ...: sqlite store: bead
// store closed" (order_dispatch.go emits it at the tracking-bead close). The
// orders lane was the VICTIM there; these tests pin that it is not also a
// perpetrator, so the predicate cannot be reintroduced by the obvious refactor.
//
// The three sites and the line that keeps each close list own-only:
//   - the per-tick map (order_dispatch.go dispatch's deferred closer) is written
//     only at stores[storeKey] = store and legacyCityStoreForTarget's
//     stores[key] = store, and both values come from m.storeFn —
//     openStoreAtForCityWithConfig, a factory (internal/beads/factory.go
//     OpenStoreAtForCity) with no process memo: every call constructs a store.
//     The class-resolved binding enters the tick only as a gate READ store
//     (gateStoresFor) and as the tracking-bead front door, never into the map.
//   - the webhook seam (order_dispatch_seam.go) closes the single handle its own
//     m.storeFn call returned, and nothing else; its onDone closer runs after
//     dispatchOne, which is the same #3157 shape as the tick.
//   - the tracking sweeps (city_runtime.go orderTrackingSweepStores) return
//     closeOpened over `freshlyOpened` — the slice only newCityRuntimeOpenSweepStore
//     appends to — while the runtime-owned city/rig stores and the boot-owned
//     orders binding are appended to the READ list `stores` beside it.
//
// So the ownership answer already travels with the handle at these three sites:
// what is in a close list was opened by that same call. There is deliberately no
// closable flag here the way cmd_nudge.go needed one — a nudge frame stores a
// resolved handle in a field and re-derives ownership from `Store == nil`, which
// is exactly where the lie lived; an order dispatch never re-derives, it closes
// the value it opened. These tests are the guard that keeps it that way.

// relocatedOrdersRoutes builds routes for a city that relocates the ORDERS class
// to a non-work binding, so a class resolver hands out the shared handle rather
// than the work store.
func relocatedOrdersRoutes(binding beads.Store) *storageRoutes {
	return &storageRoutes{
		stores:  map[coordclass.Class]beads.Store{coordclass.ClassOrders: binding},
		binding: "infra",
	}
}

// waitForStoreClosed waits for a store the caller EXPECTS to be released, so a
// fix that quietly stops closing its own handle (a per-tick leak instead of a
// use-after-close) fails here instead of passing.
func waitForStoreClosed(t *testing.T, store *latchedCloseStore, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !store.isClosed() {
		if time.Now().After(deadline) {
			t.Fatalf("%s was never closed — the frame leaked the handle it opened", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// assertStoreStillOpen probes a store the caller must NOT have closed with a real
// read, which is what the next caller in the process performs.
func assertStoreStillOpen(t *testing.T, store *latchedCloseStore, what string) {
	t.Helper()
	if _, err := store.Get("cr-lau2b-liveness-probe"); errors.Is(err, beads.ErrStoreClosed) {
		t.Errorf("%s was closed by this frame; every later caller in the process now reads a closed handle: %v", what, err)
	}
}

func lau2bExecOK(context.Context, string, string, []string) ([]byte, error) {
	return []byte("ok"), nil
}

// TestPerTickOrderDispatchCloseNeverClosesTheOrdersBinding pins the per-tick
// closer: it releases the handle the tick opened and leaves the orders-class
// binding the same tick writes its tracking beads to.
func TestPerTickOrderDispatchCloseNeverClosesTheOrdersBinding(t *testing.T) {
	cityPath := t.TempDir()
	var tickLog bytes.Buffer
	perTick := newLatchedCloseStore()
	binding := newLatchedCloseStore()

	eventLog := events.NewFake()
	eventLog.Record(events.Event{Type: events.BeadClosed, Actor: "test"})

	ad := buildOrderDispatcherFromListExec([]orders.Order{{
		Name:    "lau2b-tick",
		Trigger: "event",
		On:      events.BeadClosed,
		Exec:    "true",
	}}, perTick, eventLog, lau2bExecOK, events.Discard)
	if ad == nil {
		t.Fatal("expected non-nil dispatcher")
	}
	mad := ad.(*memoryOrderDispatcher)
	mad.cityPath = cityPath
	mad.stderr = lockedStderr(&tickLog)
	mad.storageRoutes = relocatedOrdersRoutes(beads.Store(binding))

	mad.dispatch(context.Background(), cityPath, time.Now())

	drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !mad.drain(drainCtx) {
		t.Fatal("drain timed out waiting for in-flight dispatchOne to finish")
	}

	waitForStoreClosed(t, perTick, "the per-tick order store")
	assertStoreStillOpen(t, binding, "the relocated orders-class binding")
	if binding.isClosed() {
		t.Error("the per-tick closer closed the orders-class binding (cr-ypk5s predicate)")
	}

	// Non-vacuity: the dispatch really wrote its tracking bead through the class
	// resolver, so the assertion above guarded a handle the tick held.
	if got := trackingBeads(t, beads.Store(binding), "order-run:lau2b-tick"); len(got) == 0 {
		t.Fatalf("the dispatch wrote no tracking bead to the orders binding, so the assertion above guarded nothing. dispatcher log:\n%s", tickLog.String())
	}
}

// TestWebhookDispatchSeamClosesOnlyTheStoreItOpened pins the same predicate on
// the orderdispatch.Dispatcher seam the webhook sink fires through: the seam's
// closeStore releases its own open, not the binding the tracking bead lives in.
func TestWebhookDispatchSeamClosesOnlyTheStoreItOpened(t *testing.T) {
	cityPath := t.TempDir()
	opened := newLatchedCloseStore()
	binding := newLatchedCloseStore()

	order := orders.Order{Name: "lau2b-webhook", Trigger: "webhook", Exec: "true"}
	ad := buildOrderDispatcherFromListExec([]orders.Order{order}, opened, nil, lau2bExecOK, events.Discard)
	if ad == nil {
		t.Fatal("expected non-nil dispatcher")
	}
	mad := ad.(*memoryOrderDispatcher)
	mad.cityPath = cityPath
	mad.storageRoutes = relocatedOrdersRoutes(beads.Store(binding))

	res, err := mad.Dispatch(context.Background(), orderdispatch.DispatchRequest{Order: order})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !res.Fired {
		t.Fatalf("Dispatch did not fire the order: %+v", res)
	}

	drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !mad.drain(drainCtx) {
		t.Fatal("drain timed out waiting for the webhook dispatchOne to finish")
	}

	if res.TrackingID == "" {
		t.Fatal("the seam fired without a tracking bead id; this test proved nothing")
	}
	if got := trackingBeads(t, beads.Store(binding), "order-run:lau2b-webhook"); len(got) == 0 {
		t.Fatal("the seam wrote no tracking bead to the orders binding, so the assertion below guarded nothing")
	}
	waitForStoreClosed(t, opened, "the store the webhook seam opened")
	assertStoreStillOpen(t, binding, "the relocated orders-class binding")
	if binding.isClosed() {
		t.Error("the webhook seam closed the orders-class binding (cr-ypk5s predicate)")
	}
}

// TestOrderTrackingSweepCloseOpensOnlyStoresItOpened pins the sweep predicate:
// closeOpened releases only a store the sweep opened for a scope the runtime does
// not hold, and leaves both the runtime's city store and the boot-owned orders
// binding open. Closing the first would take the controller's own work ledger
// down for the rest of its life; closing the second is the nudge failure wearing
// the orders label.
func TestOrderTrackingSweepCloseOpensOnlyStoresItOpened(t *testing.T) {
	cityPath := t.TempDir()
	rigPath := filepath.Join(cityPath, "frontend")
	cityStore := newLatchedCloseStore()
	freshRig := newLatchedCloseStore()
	binding := newLatchedCloseStore()

	prevOpenSweepStore := newCityRuntimeOpenSweepStore
	newCityRuntimeOpenSweepStore = func(scopeRoot, _ string) (beads.Store, error) {
		if filepath.Clean(scopeRoot) != filepath.Clean(rigPath) {
			return nil, errors.New("unexpected sweep open for " + scopeRoot)
		}
		return beads.Store(freshRig), nil
	}
	t.Cleanup(func() { newCityRuntimeOpenSweepStore = prevOpenSweepStore })

	cr := &CityRuntime{
		cityPath: cityPath,
		cityName: "test-city",
		cfg: &config.City{
			Workspace: config.Workspace{Name: "test-city"},
			Rigs:      []config.Rig{{Name: "frontend", Path: rigPath}},
		},
		standaloneCityStore: beads.Store(cityStore),
		standaloneRigStores: map[string]beads.Store{},
		storageRoutes:       relocatedOrdersRoutes(beads.Store(binding)),
		stdout:              io.Discard,
		stderr:              io.Discard,
		logPrefix:           "gc test",
	}

	stores, _, closeOpened, err := cr.orderTrackingSweepStores()
	if err != nil {
		t.Fatalf("orderTrackingSweepStores: %v", err)
	}
	var sawFreshRig, sawBinding bool
	for _, store := range stores {
		switch unwrapOrderTrackingSweepStore(store) {
		case beads.Store(freshRig):
			sawFreshRig = true
		case beads.Store(binding):
			sawBinding = true
		}
	}
	if !sawFreshRig || !sawBinding {
		t.Fatalf("the sweep read %d stores but not both the freshly opened rig store and the orders binding (fresh=%v binding=%v)",
			len(stores), sawFreshRig, sawBinding)
	}

	closeOpened()

	waitForStoreClosed(t, freshRig, "the store the sweep opened for a scope the runtime does not hold")
	assertStoreStillOpen(t, cityStore, "the runtime's own city bead store")
	assertStoreStillOpen(t, binding, "the boot-owned orders-class binding")
	if cityStore.isClosed() {
		t.Error("closeOpened closed a store the runtime owns for its whole life (city_runtime.go orderTrackingSweepStores doc)")
	}
	if binding.isClosed() {
		t.Error("closeOpened closed the routes-owned orders-class binding (cr-ypk5s predicate)")
	}
}
