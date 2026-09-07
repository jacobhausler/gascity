package api

import (
	"context"
	"fmt"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionrouter "github.com/gastownhall/gascity/internal/runtime/router"
	"github.com/gastownhall/gascity/internal/session"
)

func fixedRuntimeFactory(providers map[string]runtime.Provider) sessionrouter.Factory {
	return func(name string) (runtime.Provider, error) {
		sp, ok := providers[name]
		if !ok {
			return nil, fmt.Errorf("no such runtime %q", name)
		}
		return sp, nil
	}
}

// The API create path resolves, stamps, and routes in one step, so a session
// materialized here reaches its own backend on the very next lifecycle op —
// no reconciler pass in between. Before the seam existed the stamp landed on
// the bead but the route did not exist until the provider was re-seeded, so
// the create ran on the city default backend, which hosts a different box.
func TestAPICreatedSessionRoutesToStampedRuntimeImmediately(t *testing.T) {
	def, lane := runtime.NewFake(), runtime.NewFake()
	sp := sessionrouter.New(def, fixedRuntimeFactory(map[string]runtime.Provider{"second": lane}))

	cfg := &config.City{
		Rigs:   []config.Rig{{Name: "rig-a", RuntimeProvider: "second"}},
		Agents: []config.Agent{{Name: "worker", Dir: "rig-a"}},
	}
	meta := map[string]string{}

	rt := stampAndRouteSessionRuntime(cfg, &cfg.Agents[0], sp, "s-worker", meta)

	if rt != "second" {
		t.Fatalf("resolved runtime = %q, want %q", rt, "second")
	}
	if got := meta[session.RuntimeProviderMetadataKey]; got != "second" {
		t.Fatalf("stamped %s = %q, want %q", session.RuntimeProviderMetadataKey, got, "second")
	}
	// No re-seed, no reconcile: the create itself must land on the lane.
	if err := sp.Start(context.Background(), "s-worker", runtime.Config{Command: "c"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !lane.IsRunning("s-worker") {
		t.Fatal("lane backend does not host the session created through the API path")
	}
	if def.IsRunning("s-worker") {
		t.Fatal("default backend hosts a session stamped for another runtime")
	}
	// A later op is routed too, not just the create.
	if err := sp.Stop("s-worker"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if lane.IsRunning("s-worker") {
		t.Fatal("Stop did not reach the lane backend")
	}
}

// An agent that selects no lane-scoped runtime must be neither stamped nor
// routed: the city keeps its single provider and byte-identical metadata.
func TestAPISessionWithoutLaneRuntimeIsNotStampedOrRouted(t *testing.T) {
	def, lane := runtime.NewFake(), runtime.NewFake()
	sp := sessionrouter.New(def, fixedRuntimeFactory(map[string]runtime.Provider{"second": lane}))

	cfg := &config.City{
		Rigs:   []config.Rig{{Name: "rig-a"}},
		Agents: []config.Agent{{Name: "worker", Dir: "rig-a"}},
	}
	meta := map[string]string{}

	if rt := stampAndRouteSessionRuntime(cfg, &cfg.Agents[0], sp, "s-worker", meta); rt != "" {
		t.Fatalf("resolved runtime = %q, want empty", rt)
	}
	if len(meta) != 0 {
		t.Fatalf("metadata written for an unselected runtime: %v", meta)
	}
	if err := sp.Start(context.Background(), "s-worker", runtime.Config{Command: "c"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !def.IsRunning("s-worker") {
		t.Fatal("unstamped session did not stay on the default backend")
	}
}

// A provider that does not route runtimes (every city today that sets no
// lane-scoped runtime) must not be disturbed by the seam.
func TestRouteSessionRuntimeIgnoresNonRoutingProvider(t *testing.T) {
	sp := runtime.NewFake()
	routeSessionRuntime(sp, "s-worker", "second")
	routeSessionRuntime(nil, "s-worker", "second")
	routeSessionRuntime(sp, "", "second")
	routeSessionRuntime(sp, "s-worker", "")
	if sp.IsRunning("s-worker") {
		t.Fatal("routing touched the provider")
	}
}

// The seam must be the RouteRuntime analogue of the RouteACP seam: both
// capabilities are asserted, never a concrete provider type, so the runtime
// router can wrap the transport router and vice versa.
func TestRuntimeRouterSatisfiesBothRoutingSeams(t *testing.T) {
	sp := sessionrouter.New(runtime.NewFake(), fixedRuntimeFactory(nil))
	if _, ok := runtime.Provider(sp).(runtimeRoutingProvider); !ok {
		t.Fatal("runtime router does not satisfy runtimeRoutingProvider")
	}
	if _, ok := runtime.Provider(sp).(acpRoutingProvider); !ok {
		t.Fatal("runtime router does not satisfy acpRoutingProvider")
	}
}
