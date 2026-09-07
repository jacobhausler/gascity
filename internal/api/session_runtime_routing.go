package api

import (
	"strings"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

// runtimeRoutingProvider is the runtime-routing analog of
// [acpRoutingProvider]: a session provider that can bind one session name to a
// specific runtime backend. Only the composite runtime router implements it;
// every other provider hosts a single backend and needs no route.
type runtimeRoutingProvider interface {
	RouteRuntime(sessionName, runtimeProvider string)
}

// routeSessionRuntime binds a session to its lane-scoped runtime backend on a
// provider that routes by runtime.
//
// This is the create-time half of routing. Provider construction seeds routes
// from the stamps of sessions that already exist, so without this call a
// session created here would be routed only once the next reconciler pass
// rebuilt or re-seeded the provider — and every lifecycle op in between, the
// create included, would land on the city default backend, which hosts a
// different box.
//
// No-op when nothing is stamped or when the provider does not route runtimes,
// so a city that selects no lane-scoped runtime behaves exactly as before.
func routeSessionRuntime(sp runtime.Provider, sessionName, runtimeProvider string) {
	sessionName = strings.TrimSpace(sessionName)
	runtimeProvider = strings.TrimSpace(runtimeProvider)
	if sessionName == "" || runtimeProvider == "" {
		return
	}
	if router, ok := sp.(runtimeRoutingProvider); ok {
		router.RouteRuntime(sessionName, runtimeProvider)
	}
}

// stampAndRouteSessionRuntime resolves an agent's lane-scoped runtime, records
// it on the session's creation metadata, and routes the session to it in one
// step. Stamping without routing leaves a new session on the default backend
// until a later reconciler pass, so the two always happen together.
//
// Returns the resolved runtime, "" when the agent selects none.
func stampAndRouteSessionRuntime(cfg *config.City, a *config.Agent, sp runtime.Provider, sessionName string, meta map[string]string) string {
	rt := config.AgentRuntimeProviderOverrideValue(cfg, a)
	if rt == "" {
		return ""
	}
	if meta != nil {
		meta[session.RuntimeProviderMetadataKey] = rt
	}
	routeSessionRuntime(sp, sessionName, rt)
	return rt
}
