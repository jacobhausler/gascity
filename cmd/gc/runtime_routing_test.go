package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionrouter "github.com/gastownhall/gascity/internal/runtime/router"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

func laneRuntimeCity() *config.City {
	cfg := &config.City{
		Rigs: []config.Rig{{Name: "alpha", Path: "/tmp/alpha"}},
		Agents: []config.Agent{
			{Name: "lane", Dir: "alpha", RuntimeProvider: "subprocess"},
			{Name: "plain", Dir: "alpha"},
		},
	}
	return cfg
}

func laneRuntimeContext(cfg *config.City) sessionProviderContext {
	return sessionProviderContext{cfg: cfg, cityName: "testcity"}
}

// A city that never sets runtime_provider must get its provider back unwrapped:
// no router, no new construction, no behavior change.
func TestComposeLaneRuntimeProviderIsInertWithoutLaneConfig(t *testing.T) {
	base := runtime.NewFake()
	cfg := &config.City{Agents: []config.Agent{{Name: "plain"}}}
	got := composeLaneRuntimeProvider(laneRuntimeContext(cfg), nil, base)
	if got != runtime.Provider(base) {
		t.Fatalf("composeLaneRuntimeProvider wrapped a city with no lane-scoped runtime: %T", got)
	}
}

// GC_SESSION is the whole-city operator override and stays above lane routing.
func TestComposeLaneRuntimeProviderYieldsToSessionEnvOverride(t *testing.T) {
	t.Setenv("GC_SESSION", "fake")
	base := runtime.NewFake()
	got := composeLaneRuntimeProvider(laneRuntimeContext(laneRuntimeCity()), nil, base)
	if got != runtime.Provider(base) {
		t.Fatalf("GC_SESSION override was masked by the runtime router: %T", got)
	}
}

func TestComposeLaneRuntimeProviderRoutesConfiguredAgents(t *testing.T) {
	t.Setenv("GC_SESSION", "")
	cfg := laneRuntimeCity()
	sp := composeLaneRuntimeProvider(laneRuntimeContext(cfg), nil, runtime.NewFake())
	rp, ok := sp.(*sessionrouter.Provider)
	if !ok {
		t.Fatalf("provider is %T, want the runtime router", sp)
	}
	routes := laneRuntimeRoutes(laneRuntimeContext(cfg), nil)
	if len(routes) != 1 {
		t.Fatalf("routes = %v, want exactly the runtime-selecting agent", routes)
	}
	for name, rt := range routes {
		if rt != "subprocess" {
			t.Errorf("route %q = %q, want the agent's runtime_provider", name, rt)
		}
		if got, ok := rp.RuntimeFor(name); !ok || got != "subprocess" {
			t.Errorf("router RuntimeFor(%q) = (%q, %v), want the registered route", name, got, ok)
		}
	}
}

// A session's own stamp is authoritative: it must be routed even when the
// config that created it no longer selects a runtime (stale config), and it
// must win over the configured value for its agent.
func TestLaneRuntimeRoutesPreferSessionStamps(t *testing.T) {
	t.Setenv("GC_SESSION", "")
	cfg := &config.City{Agents: []config.Agent{{Name: "plain"}}}
	snapshot := newSessionBeadSnapshotFromInfos([]sessionpkg.Info{{
		ID:                  "s1",
		Template:            "plain",
		SessionNameMetadata: "gc-testcity-plain",
		SessionName:         "gc-testcity-plain",
		RuntimeProvider:     "subprocess",
	}})
	ctx := laneRuntimeContext(cfg)
	if !laneRuntimeRoutingActive(ctx, snapshot) {
		t.Fatal("a stamped live session did not activate runtime routing")
	}
	routes := laneRuntimeRoutes(ctx, snapshot)
	if routes["gc-testcity-plain"] != "subprocess" {
		t.Errorf("routes = %v, want the stamped session routed to its own runtime", routes)
	}
}

// An undeclared runtime must fail closed at construction rather than resolving
// through the registry's tmux fallback, which hosts a different box.
func TestLaneRuntimeFactoryRejectsUndeclaredRuntime(t *testing.T) {
	factory := laneRuntimeFactory(laneRuntimeContext(laneRuntimeCity()))
	if _, err := factory("no-such-runtime"); err == nil || !errors.Is(err, sessionrouter.ErrRuntimeUnresolvable) {
		t.Fatalf("factory error = %v, want ErrRuntimeUnresolvable", err)
	}
	sp, err := factory("fake")
	if err != nil {
		t.Fatalf("factory(fake): %v", err)
	}
	if sp == nil {
		t.Fatal("factory(fake) returned no provider")
	}
}

// End-to-end within the process: a mixed city where one lane runs on a second
// runtime and everything else stays on the default backend. Create → start →
// peek → nudge → stop must all land on the lane's own backend.
func TestMixedLaneRuntimeLifecycle(t *testing.T) {
	t.Setenv("GC_SESSION", "")
	def, lane := runtime.NewFake(), runtime.NewFake()
	rp := sessionrouter.New(def, func(name string) (runtime.Provider, error) {
		if name != "subprocess" {
			return nil, errors.New("unexpected runtime " + name)
		}
		return lane, nil
	})

	cfg := laneRuntimeCity()
	meta := map[string]string{"template": "alpha/lane"}
	stampLaneRuntimeMetadata(meta, config.AgentRuntimeProviderOverrideValue(cfg, &cfg.Agents[0]))
	if meta[sessionpkg.RuntimeProviderMetadataKey] != "subprocess" {
		t.Fatalf("session metadata = %v, want a stamped runtime_provider", meta)
	}
	plain := map[string]string{"template": "alpha/plain"}
	stampLaneRuntimeMetadata(plain, config.AgentRuntimeProviderOverrideValue(cfg, &cfg.Agents[1]))
	if _, stamped := plain[sessionpkg.RuntimeProviderMetadataKey]; stamped {
		t.Fatalf("an agent with no runtime_provider was stamped: %v", plain)
	}

	routeLaneRuntime(rp, "gc-lane", meta[sessionpkg.RuntimeProviderMetadataKey])
	ctx := context.Background()
	for _, name := range []string{"gc-lane", "gc-plain"} {
		if err := rp.Start(ctx, name, runtime.Config{Command: "c"}); err != nil {
			t.Fatalf("Start(%s): %v", name, err)
		}
	}
	if !lane.IsRunning("gc-lane") || lane.IsRunning("gc-plain") {
		t.Fatal("lane backend does not host exactly the lane-scoped session")
	}
	if !def.IsRunning("gc-plain") || def.IsRunning("gc-lane") {
		t.Fatal("default backend does not host exactly the unstamped session")
	}
	if err := rp.Nudge("gc-lane", runtime.TextContent("go")); err != nil {
		t.Fatalf("Nudge: %v", err)
	}
	if _, err := rp.Peek("gc-lane", 5); err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if err := rp.Stop("gc-lane"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	for _, op := range []string{"Nudge", "Peek", "Stop"} {
		if got := def.CountCalls(op, "gc-lane"); got != 0 {
			t.Errorf("default backend saw %d %s calls for the lane session, want 0", got, op)
		}
		if got := lane.CountCalls(op, "gc-lane"); got != 1 {
			t.Errorf("lane backend %s calls = %d, want 1", op, got)
		}
	}
}

// The reconciler stamps from the resolved template params, so an agent's rig
// default must reach the session record too.
func TestLaneRuntimeProviderForAgentUsesRigDefault(t *testing.T) {
	rigs := []config.Rig{{Name: "alpha", Path: "/tmp/alpha", RuntimeProvider: "subprocess"}}
	agentCfg := &config.Agent{Name: "worker", Dir: "alpha"}
	if got := laneRuntimeProviderForAgent(nil, rigs, agentCfg); got != "subprocess" {
		t.Errorf("laneRuntimeProviderForAgent = %q, want the rig default", got)
	}
	agentCfg.RuntimeProvider = "k8s"
	if got := laneRuntimeProviderForAgent(nil, rigs, agentCfg); got != "k8s" {
		t.Errorf("laneRuntimeProviderForAgent = %q, want the agent override", got)
	}
	if got := laneRuntimeProviderForAgent(nil, rigs, &config.Agent{Name: "citywide"}); got != "" {
		t.Errorf("laneRuntimeProviderForAgent = %q, want no lane-scoped selection", got)
	}
}

func TestRuntimeProviderMetadataKeyIsNotOverlayable(t *testing.T) {
	err := sessionpkg.ValidateOverlay(
		map[string]string{sessionpkg.RuntimeProviderMetadataKey: "elsewhere"},
		[]string{sessionpkg.RuntimeProviderMetadataKey},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), sessionpkg.RuntimeProviderMetadataKey) {
		t.Fatalf("ValidateOverlay error = %v, want the stamped runtime to be un-overridable", err)
	}
}

// Every CLI/pool/API create site stamps and routes in the same breath. This is
// the create-time property that pairing buys: a session created now runs on its
// own backend on the very next op, with no reconciler pass in between (the
// provider's construction-time seeding is what used to be the only source of
// routes, and it only runs on rebuild/re-seed).
func TestCreateSiteStampAndRouteRoutesBeforeAnyReconcile(t *testing.T) {
	t.Setenv("GC_SESSION", "")
	def, lane := runtime.NewFake(), runtime.NewFake()
	rp := sessionrouter.New(def, func(name string) (runtime.Provider, error) {
		if name != "subprocess" {
			return nil, errors.New("unexpected runtime " + name)
		}
		return lane, nil
	})

	cfg := laneRuntimeCity()
	// Exactly what a create site does: resolve, stamp the session record, route.
	meta := map[string]string{}
	laneRuntime := config.AgentRuntimeProviderOverrideValue(cfg, &cfg.Agents[0])
	stampLaneRuntimeMetadata(meta, laneRuntime)
	routeLaneRuntime(rp, "s-created", laneRuntime)

	if meta[sessionpkg.RuntimeProviderMetadataKey] != "subprocess" {
		t.Fatalf("session metadata = %v, want a stamped runtime_provider", meta)
	}
	// No re-seed, no composeLaneRuntimeProvider rebuild: start now.
	if err := rp.Start(context.Background(), "s-created", runtime.Config{Command: "c"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !lane.IsRunning("s-created") {
		t.Fatal("the created session is not on its own backend")
	}
	if def.IsRunning("s-created") {
		t.Fatal("the created session landed on the default backend")
	}

	// The unselected agent in the same city is untouched by all of this.
	plainMeta := map[string]string{}
	plainRuntime := config.AgentRuntimeProviderOverrideValue(cfg, &cfg.Agents[1])
	stampLaneRuntimeMetadata(plainMeta, plainRuntime)
	routeLaneRuntime(rp, "s-plain", plainRuntime)
	if len(plainMeta) != 0 {
		t.Fatalf("unselected agent was stamped: %v", plainMeta)
	}
	if err := rp.Start(context.Background(), "s-plain", runtime.Config{Command: "c"}); err != nil {
		t.Fatalf("Start(plain): %v", err)
	}
	if !def.IsRunning("s-plain") {
		t.Fatal("an unstamped session left the default backend")
	}
}

func TestRouteLaneRuntimeClearsStaleRouteWhenSelectionIsEmpty(t *testing.T) {
	def, lane := runtime.NewFake(), runtime.NewFake()
	rp := sessionrouter.New(def, func(name string) (runtime.Provider, error) {
		if name != "subprocess" {
			return nil, errors.New("unexpected runtime " + name)
		}
		return lane, nil
	})

	routeLaneRuntime(rp, "s-recreated", "subprocess")
	routeLaneRuntime(rp, "s-recreated", "")

	if got, ok := rp.RuntimeFor("s-recreated"); ok {
		t.Fatalf("RuntimeFor(s-recreated) = (%q, true), want no stale route", got)
	}
	if err := rp.Start(context.Background(), "s-recreated", runtime.Config{Command: "c"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !def.IsRunning("s-recreated") {
		t.Fatal("re-created session did not return to the default backend")
	}
	if lane.IsRunning("s-recreated") {
		t.Fatal("re-created session remained on the old lane backend")
	}
}

// The rig-level default must survive a rig override that re-points Dir: the
// agent still belongs to the rig that declared the override.
func TestLaneRuntimeProviderForAgentUsesOwningRigNotDir(t *testing.T) {
	rigs := []config.Rig{
		{Name: "alpha", Path: "/tmp/alpha", RuntimeProvider: "subprocess"},
		{Name: "elsewhere", Path: "/tmp/elsewhere", RuntimeProvider: "wrong-runtime"},
	}
	agentCfg := &config.Agent{Name: "worker", Dir: "elsewhere", RigName: "alpha"}
	if got := laneRuntimeProviderForAgent(nil, rigs, agentCfg); got != "subprocess" {
		t.Errorf("laneRuntimeProviderForAgent = %q, want the owning rig's default", got)
	}
	city := &config.City{Rigs: rigs}
	if got := laneRuntimeProviderForAgent(city, nil, agentCfg); got != "subprocess" {
		t.Errorf("laneRuntimeProviderForAgent(city) = %q, want the owning rig's default", got)
	}
}
