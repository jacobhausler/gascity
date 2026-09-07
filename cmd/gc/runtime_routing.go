package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/gastownhall/gascity/internal/agent"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionrouter "github.com/gastownhall/gascity/internal/runtime/router"
	"github.com/gastownhall/gascity/internal/session"
)

// composeLaneRuntimeProvider wraps a city's session provider in the runtime
// router when — and only when — some agent or rig selects a runtime backend of
// its own, or some existing session was created under one.
//
// With no lane-scoped selection anywhere the base provider is returned
// unchanged, so a city that never sets runtime_provider constructs, routes, and
// behaves exactly as before. GC_SESSION stays above all of this: it is an
// explicit whole-city operator override, and when it is set every lane runs
// where it says (see laneRuntimeRoutingActive).
func composeLaneRuntimeProvider(ctx sessionProviderContext, sessionBeads *sessionBeadSnapshot, base runtime.Provider) runtime.Provider {
	if !laneRuntimeRoutingActive(ctx, sessionBeads) {
		return base
	}
	rp := sessionrouter.New(base, laneRuntimeFactory(ctx))
	for name, rt := range laneRuntimeRoutes(ctx, sessionBeads) {
		rp.RouteRuntime(name, rt)
	}
	return rp
}

// laneRuntimeRoutingActive reports whether this city needs runtime routing at
// all: a configured lane-scoped selection, or an open session already stamped
// with one (which must keep routing to its own box even after the config that
// created it is removed).
func laneRuntimeRoutingActive(ctx sessionProviderContext, sessionBeads *sessionBeadSnapshot) bool {
	if strings.TrimSpace(sessionEnvOverrideName()) != "" {
		// An explicit whole-city override: every session runs where the operator
		// said, including ones stamped otherwise. Routing would silently ignore
		// the override for stamped lanes.
		return false
	}
	if config.CityUsesLaneScopedRuntimes(ctx.cfg) {
		return true
	}
	return len(stampedSessionRuntimes(sessionBeads)) > 0
}

// sessionEnvOverrideName reports the GC_SESSION whole-city override, if set.
func sessionEnvOverrideName() string {
	return strings.TrimSpace(os.Getenv("GC_SESSION"))
}

// laneRuntimeFactory constructs the provider for one recorded runtime name.
//
// Unlike the city-level selection path it refuses to resolve an undeclared name
// through the registry's tmux fallback: a session recorded as running on a
// runtime this binary does not know about must fail closed, not be inspected or
// stopped through a local tmux server that hosts a different box.
func laneRuntimeFactory(ctx sessionProviderContext) sessionrouter.Factory {
	return func(runtimeName string) (runtime.Provider, error) {
		runtimeName = strings.TrimSpace(runtimeName)
		if runtimeName == "" {
			return nil, fmt.Errorf("%w: empty runtime name", sessionrouter.ErrRuntimeUnresolvable)
		}
		reg, err := runtimeRegistryForCity(ctx.cfg)
		if err != nil {
			return nil, fmt.Errorf("runtime %q: %w", runtimeName, err)
		}
		if !reg.Resolves(runtimeName) {
			return nil, fmt.Errorf("%w: %q is not a registered runtime (declare it as a [runtimes.%s] pack runtime, or use a builtin selection name)",
				sessionrouter.ErrRuntimeUnresolvable, runtimeName, runtimeName)
		}
		return reg.New(runtimeName, ctx.sc, ctx.cityName, ctx.cityPath)
	}
}

// laneRuntimeRoutes returns the session-name → runtime routes known at
// provider-construction time: every open session's own stamp, plus the
// deterministic names of configured agents that select a runtime (so a session
// created later in this process starts on the right backend).
//
// A session's stamp always wins over the configured value for its agent: the
// stamp records where the box actually is.
func laneRuntimeRoutes(ctx sessionProviderContext, sessionBeads *sessionBeadSnapshot) map[string]string {
	routes := map[string]string{}
	for name, rt := range configuredAgentRuntimeNames(sessionBeads, ctx.cityName, ctx.sessionTemplate, ctx.cfg) {
		routes[name] = rt
	}
	for name, rt := range stampedSessionRuntimes(sessionBeads) {
		routes[name] = rt
	}
	return routes
}

// stampedSessionRuntimes returns the recorded runtime of every open session
// that carries one.
func stampedSessionRuntimes(sessionBeads *sessionBeadSnapshot) map[string]string {
	if sessionBeads == nil {
		return nil
	}
	var routes map[string]string
	for _, info := range sessionBeads.OpenInfos() {
		rt := strings.TrimSpace(info.RuntimeProvider)
		sessionName := strings.TrimSpace(info.SessionNameMetadata)
		if rt == "" || sessionName == "" {
			continue
		}
		if routes == nil {
			routes = map[string]string{}
		}
		routes[sessionName] = rt
	}
	return routes
}

// configuredAgentRuntimeNames maps the deterministic session name of each
// runtime-selecting agent to its selection, mirroring configuredACPSessionNames
// (including the snapshot's recorded name when one exists).
func configuredAgentRuntimeNames(snapshot *sessionBeadSnapshot, cityName, sessionTemplate string, cfg *config.City) map[string]string {
	if cfg == nil {
		return nil
	}
	var routes map[string]string
	for i := range cfg.Agents {
		agentCfg := cfg.Agents[i]
		rt, _ := config.AgentRuntimeProviderOverride(cfg, &agentCfg)
		if rt == "" {
			continue
		}
		sessName := agent.SessionNameFor(cityName, agentCfg.QualifiedName(), sessionTemplate)
		if snapshot != nil {
			if beadName := snapshot.FindSessionNameByTemplate(agentCfg.QualifiedName()); beadName != "" {
				sessName = beadName
			}
		}
		if sessName == "" {
			continue
		}
		if routes == nil {
			routes = map[string]string{}
		}
		routes[sessName] = rt
	}
	return routes
}

// laneRuntimeProviderForAgent resolves an agent's lane-scoped runtime from an
// explicit rig list rather than from a whole city, for the desired-state build,
// which carries rigs directly and may hold no city struct at all.
func laneRuntimeProviderForAgent(city *config.City, rigs []config.Rig, a *config.Agent) string {
	if a == nil {
		return ""
	}
	if rt := strings.TrimSpace(a.RuntimeProvider); rt != "" {
		return rt
	}
	// Key on the owning rig, not on Dir: a rig override may re-point Dir, and a
	// Dir-keyed lookup would then miss the rig's own default.
	rigName := a.OwningRig()
	for i := range rigs {
		if rigs[i].Name == rigName {
			return strings.TrimSpace(rigs[i].RuntimeProvider)
		}
	}
	if city != nil {
		return config.RigRuntimeProvider(city, rigName)
	}
	return ""
}

// runtimeProviderForAgentTemplate resolves the lane-scoped runtime to stamp on
// a new session for a configured agent template. It returns "" when the agent
// (and its rig) select none, which leaves the session on the city-wide provider
// exactly as before.
func runtimeProviderForAgentTemplate(cfg *config.City, templateName string) string {
	if cfg == nil {
		return ""
	}
	agentCfg := findAgentByTemplate(cfg, templateName)
	if agentCfg == nil {
		return ""
	}
	rt, _ := config.AgentRuntimeProviderOverride(cfg, agentCfg)
	return rt
}

// stampLaneRuntimeMetadata records the resolved lane-scoped runtime on a
// session's creation metadata. It is a no-op when nothing is selected, so
// unstamped sessions stay byte-identical to what earlier binaries wrote.
func stampLaneRuntimeMetadata(meta map[string]string, runtimeProvider string) {
	runtimeProvider = strings.TrimSpace(runtimeProvider)
	if meta == nil || runtimeProvider == "" {
		return
	}
	meta[session.RuntimeProviderMetadataKey] = runtimeProvider
}

// routeLaneRuntime registers a session's runtime with a provider that routes by
// it. Dynamically created sessions reach the router this way, the same way
// dynamic ACP sessions reach the transport router via RouteACP.
func routeLaneRuntime(sp runtime.Provider, sessionName, runtimeProvider string) {
	sessionName = strings.TrimSpace(sessionName)
	runtimeProvider = strings.TrimSpace(runtimeProvider)
	if sessionName == "" || runtimeProvider == "" {
		return
	}
	if router, ok := sp.(interface{ RouteRuntime(string, string) }); ok {
		router.RouteRuntime(sessionName, runtimeProvider)
	}
}
