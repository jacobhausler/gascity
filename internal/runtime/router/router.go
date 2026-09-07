// Package router provides a composite [runtime.Provider] that routes each
// session to the runtime backend recorded for it, falling back to a default
// backend for sessions with no recorded runtime.
//
// It is the lifecycle half of per-agent runtime selection: a session's runtime
// is resolved once (at creation) and stamped on the session record, and every
// later operation on that session — stop, peek, nudge, status, cleanup — is
// routed through the stamped runtime, even if configuration has changed since.
//
// Two properties distinguish it from the transport router in
// [github.com/gastownhall/gascity/internal/runtime/auto]:
//
//   - It is fail-closed. When a session carries a recorded runtime that cannot
//     be constructed, every operation on that session reports the resolution
//     error. The session is never silently inspected or stopped through the
//     default backend, which would act on the wrong box.
//   - Backends are isolated. One provider instance is constructed per distinct
//     runtime name, lazily, and a failure (or a hang) in one backend's own
//     calls cannot affect sessions routed elsewhere: no lock is held across a
//     backend call, and [Provider.ListRunning] merges per-backend results so an
//     unreachable remote runtime degrades to a partial listing instead of an
//     error for every lane.
//
// A Provider with no routes registered behaves exactly like its default
// backend.
package router

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
)

// ErrRuntimeUnresolvable reports that a session records a runtime backend this
// process cannot construct — the runtime is not registered, or its constructor
// failed. Operations on such a session return it instead of falling back to the
// default backend, which hosts a different box.
var ErrRuntimeUnresolvable = errors.New("recorded session runtime cannot be resolved")

// Factory constructs the provider for a runtime selection name. It is called
// at most once per distinct name per Provider; the result (or the error) is
// memoized.
type Factory func(runtimeName string) (runtime.Provider, error)

type backend struct {
	once sync.Once
	sp   runtime.Provider
	err  error
}

// Provider routes session operations to the backend recorded for each session.
type Provider struct {
	defaultSP runtime.Provider
	factory   Factory

	mu       sync.RWMutex
	routes   map[string]string // session name → runtime selection name
	backends map[string]*backend
}

var (
	_ runtime.Provider                      = (*Provider)(nil)
	_ runtime.DeadRuntimeSessionChecker     = (*Provider)(nil)
	_ runtime.InteractionProvider           = (*Provider)(nil)
	_ runtime.InterruptBoundaryWaitProvider = (*Provider)(nil)
	_ runtime.InterruptedTurnResetProvider  = (*Provider)(nil)
	_ runtime.RelaunchProvider              = (*Provider)(nil)
	_ runtime.LivenessObserver              = (*Provider)(nil)
	_ runtime.TransportCapabilityProvider   = (*Provider)(nil)
)

// New creates a runtime router. defaultSP handles every session with no
// recorded runtime; factory constructs the backend for a recorded one.
func New(defaultSP runtime.Provider, factory Factory) *Provider {
	return &Provider{
		defaultSP: defaultSP,
		factory:   factory,
		routes:    make(map[string]string),
		backends:  make(map[string]*backend),
	}
}

// RouteRuntime records that the named session runs on runtimeName. An empty
// runtimeName clears the route (the session returns to the default backend).
// Must be called before the first operation on that session.
func (p *Provider) RouteRuntime(name, runtimeName string) {
	if name == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if runtimeName == "" {
		delete(p.routes, name)
		return
	}
	p.routes[name] = runtimeName
	if _, ok := p.backends[runtimeName]; !ok {
		p.backends[runtimeName] = &backend{}
	}
}

// Unroute forwards a transport-route removal to the wrapped default backend.
//
// It deliberately does NOT drop the session's runtime route. Callers of this
// method (the session manager's ACP registration undo) mean "forget the ACP
// transport routing I just added", and they run while the session is still
// alive: dropping its runtime route there would send the next operation to the
// default backend, which hosts a different box. A session's runtime route is
// cleared only when the session is destroyed, by [Provider.Stop].
func (p *Provider) Unroute(name string) {
	if router, ok := p.defaultSP.(interface{ Unroute(string) }); ok {
		router.Unroute(name)
	}
}

// clearRuntimeRoute drops a destroyed session's runtime route so entries do not
// leak. Re-created sessions are re-routed at creation, so a route that outlives
// its session is never consulted for a different box.
func (p *Provider) clearRuntimeRoute(name string) {
	p.mu.Lock()
	delete(p.routes, name)
	p.mu.Unlock()
}

// RuntimeFor reports the runtime recorded for a session, and whether one is
// recorded at all.
func (p *Provider) RuntimeFor(name string) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	rt, ok := p.routes[name]
	return rt, ok
}

// route resolves the backend for a session. Sessions with no recorded runtime
// get the default backend. A recorded runtime that cannot be constructed
// returns an error: the caller must not fall back to the default backend.
func (p *Provider) route(name string) (runtime.Provider, error) {
	p.mu.RLock()
	rt, ok := p.routes[name]
	var b *backend
	if ok {
		b = p.backends[rt]
	}
	p.mu.RUnlock()
	if !ok {
		return p.defaultSP, nil
	}
	if b == nil {
		// RouteRuntime always installs the entry; a missing one means the route
		// table was mutated concurrently. Fail closed rather than guess.
		return nil, fmt.Errorf("%w: %q", ErrRuntimeUnresolvable, rt)
	}
	sp, err := p.backendProvider(rt, b)
	if err != nil {
		return nil, fmt.Errorf("session %q runtime %q: %w", name, rt, err)
	}
	return sp, nil
}

// backendProvider constructs (once) and returns the provider for a runtime
// name. The factory runs outside p.mu, so a slow backend construction never
// blocks operations on other lanes.
func (p *Provider) backendProvider(rt string, b *backend) (runtime.Provider, error) {
	b.once.Do(func() {
		if p.factory == nil {
			b.err = fmt.Errorf("%w: %q", ErrRuntimeUnresolvable, rt)
			return
		}
		sp, err := p.factory(rt)
		if err != nil {
			b.err = err
			return
		}
		if sp == nil {
			b.err = fmt.Errorf("%w: %q", ErrRuntimeUnresolvable, rt)
			return
		}
		b.sp = sp
	})
	if b.err != nil {
		return nil, b.err
	}
	return b.sp, nil
}

// routedBackends returns the default backend plus every constructed routed
// backend, labeled for merged reporting. Backends whose construction fails are
// reported as failing entries rather than dropped, so a merged listing degrades
// to partial instead of silently losing a runtime's sessions.
func (p *Provider) routedBackends() ([]runtime.BackendListResult, []string) {
	p.mu.RLock()
	names := make([]string, 0, len(p.backends))
	entries := make(map[string]*backend, len(p.backends))
	inUse := make(map[string]bool, len(p.routes))
	for _, rt := range p.routes {
		inUse[rt] = true
	}
	for rt, b := range p.backends {
		if inUse[rt] {
			names = append(names, rt)
			entries[rt] = b
		}
	}
	p.mu.RUnlock()
	sort.Strings(names)
	results := make([]runtime.BackendListResult, 0, len(names))
	for _, rt := range names {
		if _, err := p.backendProvider(rt, entries[rt]); err != nil {
			results = append(results, runtime.BackendListResult{Label: rt, Err: err})
		}
	}
	return results, names
}

// Start delegates to the routed backend.
func (p *Provider) Start(ctx context.Context, name string, cfg runtime.Config) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	return sp.Start(ctx, name, cfg)
}

// Stop delegates to the routed backend. It never falls through to another
// backend: stopping the wrong box is worse than reporting the failure.
func (p *Provider) Stop(name string) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	if stopErr := sp.Stop(name); stopErr != nil {
		return stopErr
	}
	p.clearRuntimeRoute(name)
	return nil
}

// Interrupt delegates to the routed backend.
func (p *Provider) Interrupt(name string) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	return sp.Interrupt(name)
}

// IsRunning delegates to the routed backend. An unresolvable runtime reports
// false: gc has no evidence the session is running and must not consult the
// default backend, whose answer would describe a different box.
func (p *Provider) IsRunning(name string) bool {
	sp, err := p.route(name)
	if err != nil {
		return false
	}
	return sp.IsRunning(name)
}

// IsDeadRuntimeSession delegates to the routed backend when it can positively
// distinguish live sessions from visible dead artifacts.
func (p *Provider) IsDeadRuntimeSession(name string) (bool, error) {
	sp, err := p.route(name)
	if err != nil {
		return false, err
	}
	checker, ok := sp.(runtime.DeadRuntimeSessionChecker)
	if !ok {
		return false, nil
	}
	return checker.IsDeadRuntimeSession(name)
}

// IsAttached delegates to the routed backend.
func (p *Provider) IsAttached(name string) bool {
	sp, err := p.route(name)
	if err != nil {
		return false
	}
	return sp.IsAttached(name)
}

// Attach delegates to the routed backend.
func (p *Provider) Attach(name string) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	return sp.Attach(name)
}

// ProcessAlive delegates to the routed backend.
func (p *Provider) ProcessAlive(name string, processNames []string) bool {
	sp, err := p.route(name)
	if err != nil {
		return false
	}
	return sp.ProcessAlive(name, processNames)
}

// ObserveLiveness delegates through runtime.ObserveLiveness so a backend's
// native LivenessObserver fast-path is preserved.
//
// [runtime.Liveness] carries no error channel, so an unresolvable runtime
// reports the zero value ("not observed running"). That is deliberately NOT the
// signal destructive reconciler arms key on: they gate on the partial-list
// error [Provider.ListRunning] returns when a backend is unreachable, so a
// runtime outage defers those arms instead of reaping the sessions it hosts.
func (p *Provider) ObserveLiveness(name string, processNames []string) runtime.Liveness {
	sp, err := p.route(name)
	if err != nil {
		return runtime.Liveness{}
	}
	return runtime.ObserveLiveness(sp, name, processNames)
}

// Nudge delegates to the routed backend.
func (p *Provider) Nudge(name string, content []runtime.ContentBlock) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	return sp.Nudge(name, content)
}

// NudgeNow delegates to the routed backend when it supports immediate
// injection without an internal wait-idle step.
func (p *Provider) NudgeNow(name string, content []runtime.ContentBlock) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	if np, ok := sp.(runtime.ImmediateNudgeProvider); ok {
		return np.NudgeNow(name, content)
	}
	return sp.Nudge(name, content)
}

// WaitForIdle delegates to the routed backend when it supports explicit
// idle-boundary waiting.
func (p *Provider) WaitForIdle(ctx context.Context, name string, timeout time.Duration) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	if wp, ok := sp.(runtime.IdleWaitProvider); ok {
		return wp.WaitForIdle(ctx, name, timeout)
	}
	return runtime.ErrInteractionUnsupported
}

// ResetInterruptedTurn delegates to the routed backend when it supports
// provider-native interrupted-turn discard semantics.
func (p *Provider) ResetInterruptedTurn(ctx context.Context, name string) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	if rp, ok := sp.(runtime.InterruptedTurnResetProvider); ok {
		return rp.ResetInterruptedTurn(ctx, name)
	}
	return runtime.ErrInteractionUnsupported
}

// WaitForInterruptBoundary delegates to the routed backend when it can confirm
// a provider-native interrupt boundary before the next turn is injected.
func (p *Provider) WaitForInterruptBoundary(ctx context.Context, name string, since time.Time, timeout time.Duration) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	if wp, ok := sp.(runtime.InterruptBoundaryWaitProvider); ok {
		return wp.WaitForInterruptBoundary(ctx, name, since, timeout)
	}
	return runtime.ErrInteractionUnsupported
}

// Relaunch forwards a warm-box agent relaunch to the routed backend when it
// supports one.
func (p *Provider) Relaunch(ctx context.Context, name string, cfg runtime.Config) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	if rp, ok := sp.(runtime.RelaunchProvider); ok {
		return rp.Relaunch(ctx, name, cfg)
	}
	return runtime.ErrRelaunchUnsupported
}

// Pending delegates to the routed backend when it supports structured
// interactions.
func (p *Provider) Pending(name string) (*runtime.PendingInteraction, error) {
	sp, err := p.route(name)
	if err != nil {
		return nil, err
	}
	if ip, ok := sp.(runtime.InteractionProvider); ok {
		return ip.Pending(name)
	}
	return nil, runtime.ErrInteractionUnsupported
}

// Respond delegates to the routed backend when it supports structured
// interactions.
func (p *Provider) Respond(name string, response runtime.InteractionResponse) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	if ip, ok := sp.(runtime.InteractionProvider); ok {
		return ip.Respond(name, response)
	}
	return runtime.ErrInteractionUnsupported
}

// SetMeta delegates to the routed backend.
func (p *Provider) SetMeta(name, key, value string) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	return sp.SetMeta(name, key, value)
}

// GetMeta delegates to the routed backend.
func (p *Provider) GetMeta(name, key string) (string, error) {
	sp, err := p.route(name)
	if err != nil {
		return "", err
	}
	return sp.GetMeta(name, key)
}

// RemoveMeta delegates to the routed backend.
func (p *Provider) RemoveMeta(name, key string) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	return sp.RemoveMeta(name, key)
}

// Peek delegates to the routed backend.
func (p *Provider) Peek(name string, lines int) (string, error) {
	sp, err := p.route(name)
	if err != nil {
		return "", err
	}
	return sp.Peek(name, lines)
}

// ListRunning queries the default backend and every routed backend in use,
// returning best-effort results plus a partial-list error when one backend
// fails. One unreachable runtime therefore degrades orphan detection to
// partial instead of failing the lanes hosted elsewhere.
func (p *Provider) ListRunning(prefix string) ([]string, error) {
	failed, names := p.routedBackends()
	results := make([]runtime.BackendListResult, 0, len(names)+1)
	defNames, defErr := p.defaultSP.ListRunning(prefix)
	results = append(results, runtime.BackendListResult{Label: "default", Names: defNames, Err: defErr})
	failedLabels := make(map[string]bool, len(failed))
	for _, f := range failed {
		failedLabels[f.Label] = true
	}
	results = append(results, failed...)
	for _, rt := range names {
		if failedLabels[rt] {
			continue
		}
		sp, err := p.providerForRuntime(rt)
		if err != nil {
			results = append(results, runtime.BackendListResult{Label: rt, Err: err})
			continue
		}
		rtNames, rtErr := sp.ListRunning(prefix)
		results = append(results, runtime.BackendListResult{Label: rt, Names: rtNames, Err: rtErr})
	}
	return runtime.MergeBackendListResults(results...)
}

func (p *Provider) providerForRuntime(rt string) (runtime.Provider, error) {
	p.mu.RLock()
	b := p.backends[rt]
	p.mu.RUnlock()
	if b == nil {
		return nil, fmt.Errorf("%w: %q", ErrRuntimeUnresolvable, rt)
	}
	return p.backendProvider(rt, b)
}

// GetLastActivity delegates to the routed backend.
func (p *Provider) GetLastActivity(name string) (time.Time, error) {
	sp, err := p.route(name)
	if err != nil {
		return time.Time{}, err
	}
	return sp.GetLastActivity(name)
}

// ClearScrollback delegates to the routed backend.
func (p *Provider) ClearScrollback(name string) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	return sp.ClearScrollback(name)
}

// CopyTo delegates to the routed backend.
func (p *Provider) CopyTo(name, src, relDst string) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	return sp.CopyTo(name, src, relDst)
}

// SendKeys delegates to the routed backend.
func (p *Provider) SendKeys(name string, keys ...string) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	return sp.SendKeys(name, keys...)
}

// RunLive delegates to the routed backend.
func (p *Provider) RunLive(name string, cfg runtime.Config) error {
	sp, err := p.route(name)
	if err != nil {
		return err
	}
	return sp.RunLive(name, cfg)
}

// SleepCapability reports idle sleep capability for the routed backend.
func (p *Provider) SleepCapability(name string) runtime.SessionSleepCapability {
	sp, err := p.route(name)
	if err != nil {
		return runtime.SessionSleepCapabilityDisabled
	}
	if scp, ok := sp.(runtime.SleepCapabilityProvider); ok {
		return scp.SleepCapability(name)
	}
	return runtime.SessionSleepCapabilityDisabled
}

// SupportsTransport reports whether the default backend can route the
// requested transport. Transport composition is owned by the transport router
// underneath this one; a runtime route never widens it.
func (p *Provider) SupportsTransport(transport string) bool {
	if tcp, ok := p.defaultSP.(runtime.TransportCapabilityProvider); ok {
		return tcp.SupportsTransport(transport)
	}
	return transport != "acp"
}

// RouteACP forwards transport routing to the wrapped default backend so the
// runtime router does not mask the transport router underneath it.
func (p *Provider) RouteACP(name string) {
	if router, ok := p.defaultSP.(interface{ RouteACP(string) }); ok {
		router.RouteACP(name)
	}
}

// Capabilities returns the intersection of the default backend's capabilities
// and those of every already-constructed routed backend. A capability is
// reported only if every reachable backend supports it.
func (p *Provider) Capabilities() runtime.ProviderCapabilities {
	caps := p.defaultSP.Capabilities()
	_, names := p.routedBackends()
	for _, rt := range names {
		sp, err := p.providerForRuntime(rt)
		if err != nil {
			continue
		}
		rc := sp.Capabilities()
		caps.CanReportAttachment = caps.CanReportAttachment && rc.CanReportAttachment
		caps.CanReportActivity = caps.CanReportActivity && rc.CanReportActivity
	}
	return caps
}
