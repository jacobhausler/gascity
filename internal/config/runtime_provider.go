package config

import (
	"sort"
	"strings"
)

// RuntimeProviderSource names where a resolved runtime selection came from.
type RuntimeProviderSource string

const (
	// RuntimeProviderSourceNone reports that nothing selects a runtime, so the
	// city default applies.
	RuntimeProviderSourceNone RuntimeProviderSource = ""
	// RuntimeProviderSourceAgent reports an explicit agent runtime_provider.
	RuntimeProviderSourceAgent RuntimeProviderSource = "agent"
	// RuntimeProviderSourceRig reports a rig-level runtime_provider default.
	RuntimeProviderSourceRig RuntimeProviderSource = "rig"
	// RuntimeProviderSourceCity reports the city-level [session] provider.
	RuntimeProviderSourceCity RuntimeProviderSource = "city"
)

// RigRuntimeProvider returns the rig-level runtime backend default for a rig
// name. It returns "" when the rig is unknown or sets no default.
func RigRuntimeProvider(cfg *City, rigName string) string {
	rigName = strings.TrimSpace(rigName)
	if cfg == nil || rigName == "" {
		return ""
	}
	for i := range cfg.Rigs {
		if cfg.Rigs[i].Name == rigName {
			return strings.TrimSpace(cfg.Rigs[i].RuntimeProvider)
		}
	}
	return ""
}

// OwningRig returns the name of the rig this agent belongs to.
//
// It is [Agent.RigName] when a rig-scoped override stamped one, else Dir. Dir
// is the identity prefix and normally equals the rig name, but a rig override
// may re-point it (AgentOverride.Dir) — a lookup keyed on Dir alone would then
// miss the rig the agent actually belongs to. Rig-level defaults must key on
// this, not on Dir.
func (a *Agent) OwningRig() string {
	if a == nil {
		return ""
	}
	if rig := strings.TrimSpace(a.RigName); rig != "" {
		return rig
	}
	return strings.TrimSpace(a.Dir)
}

// AgentRuntimeProviderOverride returns the lane-scoped runtime backend for an
// agent — its own runtime_provider, else its rig's default — and the source
// that supplied it. It returns ("", RuntimeProviderSourceNone) when neither is
// set, which is the signal that this agent's sessions keep the city-wide
// behavior unchanged: they are not stamped and not routed.
//
// This is deliberately narrower than [ResolveAgentRuntimeProvider]: only an
// explicit per-agent or per-rig selection makes a session lane-scoped.
func AgentRuntimeProviderOverride(cfg *City, a *Agent) (string, RuntimeProviderSource) {
	if a == nil {
		return "", RuntimeProviderSourceNone
	}
	if rt := strings.TrimSpace(a.RuntimeProvider); rt != "" {
		return rt, RuntimeProviderSourceAgent
	}
	if rt := RigRuntimeProvider(cfg, a.OwningRig()); rt != "" {
		return rt, RuntimeProviderSourceRig
	}
	return "", RuntimeProviderSourceNone
}

// AgentRuntimeProviderOverrideValue is [AgentRuntimeProviderOverride] without
// the source, for call sites that only need the value to stamp.
func AgentRuntimeProviderOverrideValue(cfg *City, a *Agent) string {
	rt, _ := AgentRuntimeProviderOverride(cfg, a)
	return rt
}

// ResolveAgentRuntimeProvider returns the full runtime selection for an agent
// in resolution order: the agent's runtime_provider, then its rig's default,
// then the city-level [session] provider. An empty result means the built-in
// default runtime.
//
// GC_SESSION is deliberately absent: it is an explicit whole-city operator
// override applied at provider construction, above every configured value.
func ResolveAgentRuntimeProvider(cfg *City, a *Agent) (string, RuntimeProviderSource) {
	if rt, src := AgentRuntimeProviderOverride(cfg, a); rt != "" {
		return rt, src
	}
	if cfg != nil {
		if rt := strings.TrimSpace(cfg.Session.Provider); rt != "" {
			return rt, RuntimeProviderSourceCity
		}
	}
	return "", RuntimeProviderSourceNone
}

// CityUsesLaneScopedRuntimes reports whether any agent or rig selects a runtime
// backend of its own. When false, runtime routing is inert and every session
// keeps using the single city-wide provider.
func CityUsesLaneScopedRuntimes(cfg *City) bool {
	if cfg == nil {
		return false
	}
	for i := range cfg.Rigs {
		if strings.TrimSpace(cfg.Rigs[i].RuntimeProvider) != "" {
			return true
		}
	}
	for i := range cfg.Agents {
		if strings.TrimSpace(cfg.Agents[i].RuntimeProvider) != "" {
			return true
		}
	}
	return false
}

// LaneScopedRuntimeNames returns the distinct runtime selection names used by
// agents and rigs, sorted for stable reporting.
func LaneScopedRuntimeNames(cfg *City) []string {
	if cfg == nil {
		return nil
	}
	seen := map[string]bool{}
	var names []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		names = append(names, v)
	}
	for i := range cfg.Rigs {
		add(cfg.Rigs[i].RuntimeProvider)
	}
	for i := range cfg.Agents {
		add(cfg.Agents[i].RuntimeProvider)
	}
	sort.Strings(names)
	return names
}
