package config

import (
	"strings"
	"testing"
)

func runtimeCity() *City {
	cfg := &City{
		Rigs: []Rig{
			{Name: "alpha", Path: "/tmp/alpha", RuntimeProvider: "rig-runtime"},
			{Name: "beta", Path: "/tmp/beta"},
		},
		Agents: []Agent{
			{Name: "own", Dir: "alpha", RuntimeProvider: "agent-runtime"},
			{Name: "inherits", Dir: "alpha"},
			{Name: "plain", Dir: "beta"},
			{Name: "citywide"},
		},
	}
	cfg.Session.Provider = "city-runtime"
	return cfg
}

func agentNamed(cfg *City, name string) *Agent {
	for i := range cfg.Agents {
		if cfg.Agents[i].Name == name {
			return &cfg.Agents[i]
		}
	}
	return nil
}

func TestResolveAgentRuntimeProviderOrder(t *testing.T) {
	cfg := runtimeCity()
	tests := []struct {
		agent      string
		wantValue  string
		wantSource RuntimeProviderSource
	}{
		{"own", "agent-runtime", RuntimeProviderSourceAgent},
		{"inherits", "rig-runtime", RuntimeProviderSourceRig},
		{"plain", "city-runtime", RuntimeProviderSourceCity},
		{"citywide", "city-runtime", RuntimeProviderSourceCity},
	}
	for _, tc := range tests {
		got, src := ResolveAgentRuntimeProvider(cfg, agentNamed(cfg, tc.agent))
		if got != tc.wantValue || src != tc.wantSource {
			t.Errorf("ResolveAgentRuntimeProvider(%s) = (%q, %q), want (%q, %q)", tc.agent, got, src, tc.wantValue, tc.wantSource)
		}
	}
}

// The stamp is the lane-scoped half of the resolution: only an agent or rig
// selection marks a session as belonging to a specific runtime. A city-wide
// [session] provider must not stamp, or every existing city would start
// recording (and pinning) a runtime it never asked for.
func TestAgentRuntimeProviderOverrideIgnoresCityProvider(t *testing.T) {
	cfg := runtimeCity()
	if got, src := AgentRuntimeProviderOverride(cfg, agentNamed(cfg, "plain")); got != "" || src != RuntimeProviderSourceNone {
		t.Errorf("AgentRuntimeProviderOverride(plain) = (%q, %q), want no lane-scoped selection", got, src)
	}
	if got := AgentRuntimeProviderOverrideValue(cfg, agentNamed(cfg, "inherits")); got != "rig-runtime" {
		t.Errorf("AgentRuntimeProviderOverrideValue(inherits) = %q, want the rig default", got)
	}
}

func TestCityUsesLaneScopedRuntimes(t *testing.T) {
	if !CityUsesLaneScopedRuntimes(runtimeCity()) {
		t.Error("city with agent/rig runtime_provider reported as not lane-scoped")
	}
	plain := &City{Agents: []Agent{{Name: "a"}}, Rigs: []Rig{{Name: "r", Path: "/tmp/r"}}}
	plain.Session.Provider = "k8s"
	if CityUsesLaneScopedRuntimes(plain) {
		t.Error("city-wide [session] provider alone reported as lane-scoped")
	}
	if CityUsesLaneScopedRuntimes(nil) {
		t.Error("nil city reported as lane-scoped")
	}
	if got := LaneScopedRuntimeNames(runtimeCity()); len(got) != 2 || got[0] != "agent-runtime" || got[1] != "rig-runtime" {
		t.Errorf("LaneScopedRuntimeNames = %v, want the sorted distinct lane runtimes", got)
	}
}

func TestValidateAgentsRejectsPaddedRuntimeProvider(t *testing.T) {
	err := ValidateAgents([]Agent{{Name: "a", RuntimeProvider: " k8s "}})
	if err == nil || !strings.Contains(err.Error(), "runtime_provider") {
		t.Fatalf("ValidateAgents error = %v, want a runtime_provider complaint", err)
	}
	if err := ValidateAgents([]Agent{{Name: "a", RuntimeProvider: "exec:/opt/runtime.sh"}}); err != nil {
		t.Fatalf("ValidateAgents rejected a valid selection name: %v", err)
	}
	if err := ValidateAgents([]Agent{{Name: "a"}}); err != nil {
		t.Fatalf("ValidateAgents rejected an agent with no runtime_provider: %v", err)
	}
}

func TestValidateRigsRejectsPaddedRuntimeProvider(t *testing.T) {
	err := ValidateRigs([]Rig{{Name: "r", Path: "/tmp/r", RuntimeProvider: "k8s "}}, "hq")
	if err == nil || !strings.Contains(err.Error(), "runtime_provider") {
		t.Fatalf("ValidateRigs error = %v, want a runtime_provider complaint", err)
	}
}

// A patch is how a lane is scoped to its own runtime without editing the pack
// that stamped the agent.
func TestAgentPatchAppliesRuntimeProvider(t *testing.T) {
	cfg := &City{
		Rigs:   []Rig{{Name: "alpha", Path: "/tmp/alpha"}},
		Agents: []Agent{{Name: "worker", Dir: "alpha"}},
	}
	value := "exec:/opt/runtime.sh"
	patches := Patches{Agents: []AgentPatch{{Rig: "alpha", Name: "worker", RuntimeProvider: &value}}}
	if err := ApplyPatches(cfg, patches); err != nil {
		t.Fatalf("ApplyPatches: %v", err)
	}
	if got := cfg.Agents[0].RuntimeProvider; got != value {
		t.Errorf("agent runtime_provider = %q, want %q", got, value)
	}
	rigValue := "subprocess"
	rigPatches := Patches{Rigs: []RigPatch{{Name: "alpha", RuntimeProvider: &rigValue}}}
	if err := ApplyPatches(cfg, rigPatches); err != nil {
		t.Fatalf("ApplyPatches(rig): %v", err)
	}
	if got := cfg.Rigs[0].RuntimeProvider; got != rigValue {
		t.Errorf("rig runtime_provider = %q, want %q", got, rigValue)
	}
}

// A rig override may re-point an agent's Dir. Dir is the identity prefix, not
// the rig membership, so the rig-level runtime default must still resolve for
// the re-dir'd agent — keyed on the owning rig, never on Dir.
func TestRigDefaultResolvesForRedirectedAgent(t *testing.T) {
	dir := "elsewhere"
	agents := []Agent{{Name: "worker", Dir: "alpha"}}
	if err := applyOverrides(agents, []AgentOverride{{Agent: "worker", Dir: &dir}}, "alpha"); err != nil {
		t.Fatalf("applyOverrides: %v", err)
	}
	if agents[0].Dir != "elsewhere" {
		t.Fatalf("Dir = %q, want the re-dir'd value", agents[0].Dir)
	}
	if got := agents[0].OwningRig(); got != "alpha" {
		t.Fatalf("OwningRig() = %q, want %q", got, "alpha")
	}
	cfg := &City{Rigs: []Rig{{Name: "alpha", RuntimeProvider: "rig-runtime"}}}
	got, src := AgentRuntimeProviderOverride(cfg, &agents[0])
	if got != "rig-runtime" || src != RuntimeProviderSourceRig {
		t.Fatalf("AgentRuntimeProviderOverride = (%q, %q), want (%q, %q)", got, src, "rig-runtime", RuntimeProviderSourceRig)
	}
	// The rig named by the re-dir'd Dir must not be consulted.
	cfg.Rigs = append(cfg.Rigs, Rig{Name: "elsewhere", RuntimeProvider: "wrong-runtime"})
	if got, _ := AgentRuntimeProviderOverride(cfg, &agents[0]); got != "rig-runtime" {
		t.Fatalf("re-dir'd Dir selected the wrong rig default: %q", got)
	}
}

// An agent's own selection still wins over its rig's default after a re-dir,
// and an unstamped agent still falls back to Dir (the common case, where Dir
// is the rig name).
func TestOwningRigFallsBackToDir(t *testing.T) {
	a := Agent{Name: "worker", Dir: "alpha"}
	if got := a.OwningRig(); got != "alpha" {
		t.Fatalf("OwningRig() = %q, want %q", got, "alpha")
	}
	a.RigName = "beta"
	if got := a.OwningRig(); got != "beta" {
		t.Fatalf("OwningRig() = %q, want the stamped rig", got)
	}
	a.RuntimeProvider = "agent-runtime"
	cfg := &City{Rigs: []Rig{{Name: "beta", RuntimeProvider: "rig-runtime"}}}
	if got, src := AgentRuntimeProviderOverride(cfg, &a); got != "agent-runtime" || src != RuntimeProviderSourceAgent {
		t.Fatalf("agent selection lost to the rig default: (%q, %q)", got, src)
	}
}

// A city-scoped (rig-less) agent must not pick up any rig default, and an
// override applied with no rig must not invent membership.
func TestOverrideWithoutRigStampsNoOwningRig(t *testing.T) {
	agents := []Agent{{Name: "worker"}}
	if err := applyOverrides(agents, []AgentOverride{{Agent: "worker"}}, ""); err != nil {
		t.Fatalf("applyOverrides: %v", err)
	}
	if got := agents[0].OwningRig(); got != "" {
		t.Fatalf("OwningRig() = %q, want empty", got)
	}
	cfg := &City{Rigs: []Rig{{Name: "alpha", RuntimeProvider: "rig-runtime"}}}
	if got, src := AgentRuntimeProviderOverride(cfg, &agents[0]); got != "" || src != RuntimeProviderSourceNone {
		t.Fatalf("city-scoped agent picked up a rig default: (%q, %q)", got, src)
	}
}
