package config

import (
	"sort"
	"strings"
)

// NomadRuntimeProvider is the selection name used by the Nomad runtime pack.
// It is kept here with the runtime-selection helpers so config composition can
// recognize the remote-runtime boundary without duplicating the literal.
const NomadRuntimeProvider = "nomad"

// RuntimeProviderEnv returns the provider environment safe to pass to the
// selected runtime. A Nomad allocation cannot resolve a host-side Codex home,
// so CODEX_HOME is deliberately omitted at that boundary; the runtime owns
// the in-allocation home. Other runtimes retain the existing provider env.
// The returned map is always detached from the resolved provider.
func RuntimeProviderEnv(provider *ResolvedProvider, runtimeProvider string) map[string]string {
	if provider == nil || len(provider.Env) == 0 {
		return nil
	}
	env := cloneStringMap(provider.Env)
	if strings.TrimSpace(runtimeProvider) == NomadRuntimeProvider {
		delete(env, "CODEX_HOME")
	}
	return env
}

// ApplyRuntimeProviderShape imposes the facts the RUNTIME owns onto a resolved
// provider, so a lane can be moved between runtimes by naming the runtime and
// nothing else.
//
// Runtime and provider are independent concepts: a provider says WHAT the agent
// is (which binary, which model endpoint, what its ready prompt looks like); a
// runtime says WHERE it runs. Three resolved fields are not provider facts at
// all once the session runs in a Nomad allocation:
//
//	Args           argv never reaches the agent — the box launches gc's own
//	               rendered command, so orchestrator-shaped flags are dead weight
//	               that only make the config lie about what runs
//	SupportsHooks  gc's hooks are not installed inside the box
//	PromptMode     priming cannot ride argv here; the prompt arrives over tmux
//
// Before this existed, every lane onboarded to Nomad needed a hand-written
// WRAPPER provider restating those three as if they were properties of the
// agent — and getting them wrong produced no error at all: a live box, a live
// tmux server, a pane holding a bare shell, and a lane that claimed nothing
// (cr-8eagyh, measured 2026-09-12 onboarding the verifier lane). The wrapper was
// load-bearing config that no one could see was load-bearing.
//
// This is the same boundary RuntimeProviderEnv already polices for CODEX_HOME —
// the runtime rewriting a field it owns — widened to the rest of the shape.
// ReadyPromptPrefix and Env are deliberately untouched: those ARE provider facts
// and hold in both runtimes.
//
// Mutates in place and is safe to call more than once; a non-Nomad runtime is a
// no-op, so a lane moved back to local recovers its provider's own argv.
func ApplyRuntimeProviderShape(provider *ResolvedProvider, runtimeProvider string) {
	if provider == nil || strings.TrimSpace(runtimeProvider) != NomadRuntimeProvider {
		return
	}
	provider.Args = nil
	provider.SupportsHooks = false
	provider.PromptMode = "none"

	// Provider facts that differ by WHERE the agent runs — the model endpoint a
	// box must use instead of a host gateway, say. Merged over Env so the
	// provider's own declaration wins for this runtime only.
	if declared := provider.RuntimeEnv[NomadRuntimeProvider]; len(declared) > 0 {
		if provider.Env == nil {
			provider.Env = make(map[string]string, len(declared))
		}
		for k, v := range declared {
			provider.Env[k] = v
		}
	}
}

// RuntimeSelectionEnv returns the environment declared for a runtime selection
// under [session.runtime_env.<name>], or nil when none is declared.
//
// This is the "where it runs" half of a session's environment (cr-8eagyh). See
// SessionConfig.RuntimeEnv for why it exists and why its precedence is low. The
// returned map is always detached from the config.
func RuntimeSelectionEnv(cfg *City, runtimeProvider string) map[string]string {
	name := strings.TrimSpace(runtimeProvider)
	if cfg == nil || name == "" || len(cfg.Session.RuntimeEnv) == 0 {
		return nil
	}
	declared, ok := cfg.Session.RuntimeEnv[name]
	if !ok || len(declared) == 0 {
		return nil
	}
	return cloneStringMap(declared)
}

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
