package router

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/runtimetest"
)

func fixedFactory(providers map[string]runtime.Provider) Factory {
	return func(name string) (runtime.Provider, error) {
		sp, ok := providers[name]
		if !ok {
			return nil, fmt.Errorf("no such runtime %q", name)
		}
		return sp, nil
	}
}

func TestRouterConformance(t *testing.T) {
	var counter int64
	runtimetest.RunProviderTests(t, func(_ *testing.T) (runtime.Provider, runtime.Config, string) {
		return New(runtime.NewFake(), fixedFactory(nil)), runtime.Config{}, fmt.Sprintf("router-conform-%d", atomic.AddInt64(&counter, 1))
	})
}

// With no routes registered the router must be indistinguishable from its
// default backend — the zero-behavior-change guarantee for cities that never
// set runtime_provider.
func TestUnroutedSessionsUseDefaultBackend(t *testing.T) {
	def, other := runtime.NewFake(), runtime.NewFake()
	p := New(def, fixedFactory(map[string]runtime.Provider{"other": other}))

	if err := p.Start(context.Background(), "plain", runtime.Config{Command: "c"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !def.IsRunning("plain") {
		t.Fatal("default backend does not host the unrouted session")
	}
	if other.IsRunning("plain") {
		t.Fatal("routed backend hosts an unrouted session")
	}
	if !p.IsRunning("plain") {
		t.Fatal("router reports the unrouted session as not running")
	}
}

// Mixed providers: two sessions, two backends, one router. Every lifecycle op
// must land on the backend the session was routed to.
func TestMixedRuntimesRouteEachLifecycleOp(t *testing.T) {
	def, second := runtime.NewFake(), runtime.NewFake()
	p := New(def, fixedFactory(map[string]runtime.Provider{"second": second}))
	p.RouteRuntime("lane", "second")

	ctx := context.Background()
	if err := p.Start(ctx, "local", runtime.Config{Command: "c"}); err != nil {
		t.Fatalf("Start(local): %v", err)
	}
	if err := p.Start(ctx, "lane", runtime.Config{Command: "c"}); err != nil {
		t.Fatalf("Start(lane): %v", err)
	}
	if !def.IsRunning("local") || def.IsRunning("lane") {
		t.Fatal("default backend does not host exactly the default-routed session")
	}
	if !second.IsRunning("lane") || second.IsRunning("local") {
		t.Fatal("routed backend does not host exactly the routed session")
	}

	if err := p.Nudge("lane", runtime.TextContent("hi")); err != nil {
		t.Fatalf("Nudge: %v", err)
	}
	if got := second.CountCalls("Nudge", "lane"); got != 1 {
		t.Errorf("routed backend Nudge calls = %d, want 1", got)
	}
	if got := def.CountCalls("Nudge", "lane"); got != 0 {
		t.Errorf("default backend saw %d Nudge calls for a routed session, want 0", got)
	}
	if _, err := p.Peek("lane", 10); err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if got := second.CountCalls("Peek", "lane"); got != 1 {
		t.Errorf("routed backend Peek calls = %d, want 1", got)
	}
	if err := p.Stop("lane"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if second.IsRunning("lane") {
		t.Error("routed backend still hosts the stopped session")
	}
	if got := def.CountCalls("Stop", "lane"); got != 0 {
		t.Errorf("default backend saw %d Stop calls for a routed session, want 0", got)
	}
}

// A session whose recorded runtime cannot be constructed must fail closed:
// every operation reports the error and none of them silently act on the
// default backend, which hosts a different box.
func TestUnresolvableRuntimeFailsClosed(t *testing.T) {
	def := runtime.NewFake()
	p := New(def, fixedFactory(nil))
	p.RouteRuntime("lane", "gone")

	if err := p.Start(context.Background(), "lane", runtime.Config{Command: "c"}); err == nil {
		t.Fatal("Start on an unresolvable runtime returned nil error")
	}
	if err := p.Stop("lane"); err == nil {
		t.Fatal("Stop on an unresolvable runtime returned nil error")
	}
	if err := p.Nudge("lane", runtime.TextContent("x")); err == nil {
		t.Fatal("Nudge on an unresolvable runtime returned nil error")
	}
	if _, err := p.Peek("lane", 5); err == nil {
		t.Fatal("Peek on an unresolvable runtime returned nil error")
	}
	if p.IsRunning("lane") {
		t.Error("IsRunning reported true for an unresolvable runtime")
	}
	if names, err := def.ListRunning(""); err != nil || len(names) != 0 {
		t.Errorf("default backend hosts %v (err %v) after ops on an unresolvable runtime, want none", names, err)
	}
	if got := def.CountCalls("Stop", "lane"); got != 0 {
		t.Errorf("default backend saw %d Stop calls, want 0", got)
	}
}

// A factory that fails is consulted once; the failure is memoized so a broken
// runtime does not re-run construction on every op.
func TestFailedProviderConstructionIsMemoized(t *testing.T) {
	var calls int64
	p := New(runtime.NewFake(), func(string) (runtime.Provider, error) {
		atomic.AddInt64(&calls, 1)
		return nil, errors.New("boom")
	})
	p.RouteRuntime("lane", "broken")

	for i := 0; i < 3; i++ {
		if err := p.Interrupt("lane"); err == nil {
			t.Fatal("Interrupt on a failing runtime returned nil error")
		}
	}
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Errorf("factory called %d times, want 1", got)
	}
}

// One unreachable runtime must not fail the lanes hosted elsewhere: ListRunning
// still returns the reachable backends' sessions, with a partial-list error.
func TestListRunningIsolatesBackendFailures(t *testing.T) {
	def := runtime.NewFake()
	if err := def.Start(context.Background(), "local", runtime.Config{Command: "c"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	p := New(def, fixedFactory(nil))
	p.RouteRuntime("lane", "unreachable")

	names, err := p.ListRunning("")
	if err == nil {
		t.Fatal("ListRunning with an unreachable backend returned nil error")
	}
	var partial *runtime.PartialListError
	if !errors.As(err, &partial) {
		t.Fatalf("ListRunning error = %v, want a PartialListError", err)
	}
	if len(names) != 1 || names[0] != "local" {
		t.Errorf("ListRunning names = %v, want the reachable backend's sessions", names)
	}
}

// A stale config that no longer selects a runtime must not move a live session:
// the route recorded at creation stays authoritative until the session ends.
func TestStaleConfigDoesNotMoveALiveSession(t *testing.T) {
	def, second := runtime.NewFake(), runtime.NewFake()
	p := New(def, fixedFactory(map[string]runtime.Provider{"second": second}))
	p.RouteRuntime("lane", "second")
	if err := p.Start(context.Background(), "lane", runtime.Config{Command: "c"}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Configuration changed: the agent no longer selects a runtime. Nothing
	// re-routes a live session, so its ops must still reach the same box.
	if err := p.Nudge("lane", runtime.TextContent("still here")); err != nil {
		t.Fatalf("Nudge: %v", err)
	}
	if got := second.CountCalls("Nudge", "lane"); got != 1 {
		t.Errorf("routed backend Nudge calls = %d, want 1", got)
	}
	if got, ok := p.RuntimeFor("lane"); !ok || got != "second" {
		t.Errorf("RuntimeFor(lane) = (%q, %v), want (\"second\", true)", got, ok)
	}
}

// Unroute carries the transport-router meaning (drop the ACP registration) and
// must not strip a live session's runtime route.
func TestUnrouteKeepsRuntimeRoute(t *testing.T) {
	second := runtime.NewFake()
	p := New(runtime.NewFake(), fixedFactory(map[string]runtime.Provider{"second": second}))
	p.RouteRuntime("lane", "second")

	p.Unroute("lane")

	if got, ok := p.RuntimeFor("lane"); !ok || got != "second" {
		t.Fatalf("RuntimeFor(lane) after Unroute = (%q, %v), want the route intact", got, ok)
	}
}

// Stop is the one place a route is retired, so entries do not accumulate.
func TestStopClearsRuntimeRoute(t *testing.T) {
	second := runtime.NewFake()
	p := New(runtime.NewFake(), fixedFactory(map[string]runtime.Provider{"second": second}))
	p.RouteRuntime("lane", "second")
	if err := p.Start(context.Background(), "lane", runtime.Config{Command: "c"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.Stop("lane"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, ok := p.RuntimeFor("lane"); ok {
		t.Error("runtime route survived Stop")
	}
}

// The transport router underneath must stay reachable: a runtime-routed city
// still needs per-session ACP registration to land on the auto provider.
type acpRecorder struct {
	runtime.Provider
	mu    sync.Mutex
	names []string
}

func (r *acpRecorder) RouteACP(name string) {
	r.mu.Lock()
	r.names = append(r.names, name)
	r.mu.Unlock()
}

func (r *acpRecorder) Unroute(name string) {
	r.mu.Lock()
	r.names = append(r.names, "unroute:"+name)
	r.mu.Unlock()
}

func TestForwardsTransportRoutingToDefaultBackend(t *testing.T) {
	rec := &acpRecorder{Provider: runtime.NewFake()}
	p := New(rec, fixedFactory(nil))

	p.RouteACP("acp-session")
	p.Unroute("acp-session")

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if strings.Join(rec.names, ",") != "acp-session,unroute:acp-session" {
		t.Errorf("forwarded transport calls = %v, want the RouteACP and Unroute pair", rec.names)
	}
}
