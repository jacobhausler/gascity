package config

import (
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/agentgroup"
)

func groupCity(groups ...AgentGroup) *City {
	return &City{
		Rigs: []Rig{{Name: "myrig"}, {Name: "otherrig"}},
		Agents: []Agent{
			{Name: "luna", Dir: "myrig"},
			{Name: "qwen", Dir: "myrig"},
			{Name: "luna", Dir: "otherrig"},
		},
		AgentGroups: groups,
	}
}

func heavy() AgentGroup {
	return AgentGroup{Name: "myrig/heavy", Members: []string{"myrig/luna", "myrig/qwen"}}
}

func TestValidateAgentGroupsAcceptsAWellFormedGroup(t *testing.T) {
	if err := ValidateAgentGroups(groupCity(heavy()), nil); err != nil {
		t.Fatalf("ValidateAgentGroups: %v", err)
	}
}

func TestValidateAgentGroupsIsInertWithNoGroups(t *testing.T) {
	if err := ValidateAgentGroups(groupCity(), nil); err != nil {
		t.Fatalf("ValidateAgentGroups with no groups: %v", err)
	}
	if err := ValidateAgentGroups(nil, nil); err != nil {
		t.Fatalf("ValidateAgentGroups(nil): %v", err)
	}
}

func TestValidateAgentGroupsRejections(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*City)
		wantSub string
	}{
		{
			name:    "unknown member",
			mutate:  func(c *City) { c.AgentGroups[0].Members = []string{"myrig/nobody"} },
			wantSub: "is not a configured agent",
		},
		{
			name:    "empty members",
			mutate:  func(c *City) { c.AgentGroups[0].Members = nil },
			wantSub: "members is required",
		},
		{
			name:    "duplicate member",
			mutate:  func(c *City) { c.AgentGroups[0].Members = []string{"myrig/luna", "myrig/luna"} },
			wantSub: "duplicate member",
		},
		{
			name:    "members span rigs",
			mutate:  func(c *City) { c.AgentGroups[0].Members = []string{"myrig/luna", "otherrig/luna"} },
			wantSub: "must not span rigs",
		},
		{
			name:    "name collides with an agent",
			mutate:  func(c *City) { c.AgentGroups[0].Name = "myrig/luna" },
			wantSub: "collides with a configured agent",
		},
		{
			name:    "name collides with a named session",
			mutate:  func(c *City) { c.NamedSessions = []NamedSession{{Name: "heavy", Dir: "myrig", Template: "luna"}} },
			wantSub: "collides with a configured named session",
		},
		{
			name:    "name is not rig-qualified",
			mutate:  func(c *City) { c.AgentGroups[0].Name = "heavy" },
			wantSub: "must be rig-qualified",
		},
		{
			name:    "name names an unconfigured rig",
			mutate:  func(c *City) { c.AgentGroups[0].Name = "ghostrig/heavy" },
			wantSub: "is not configured",
		},
		{
			name:    "empty name",
			mutate:  func(c *City) { c.AgentGroups[0].Name = "" },
			wantSub: "name is required",
		},
		{
			name:    "padded name",
			mutate:  func(c *City) { c.AgentGroups[0].Name = " myrig/heavy" },
			wantSub: "surrounding whitespace",
		},
		{
			name:    "padded member",
			mutate:  func(c *City) { c.AgentGroups[0].Members = []string{" myrig/luna"} },
			wantSub: "surrounding whitespace",
		},
		{
			name: "duplicate group name",
			mutate: func(c *City) {
				c.AgentGroups = append(c.AgentGroups, heavy())
			},
			wantSub: "duplicate name",
		},
		{
			name: "nested group",
			mutate: func(c *City) {
				c.AgentGroups = append(c.AgentGroups, AgentGroup{Name: "myrig/outer", Members: []string{"myrig/heavy"}})
			},
			wantSub: "groups do not nest",
		},
		{
			name:    "unknown strategy fails closed",
			mutate:  func(c *City) { c.AgentGroups[0].Strategy = "least-loaded" },
			wantSub: "strategy must be one of",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := groupCity(heavy())
			tc.mutate(cfg)
			err := ValidateAgentGroups(cfg, nil)
			if err == nil {
				t.Fatalf("ValidateAgentGroups accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error = %q, want it to contain %q", err, tc.wantSub)
			}
		})
	}
}

// A member whose identity is not a fixed point of route normalization would be
// silently re-routed to a different agent after it is stamped, so it is
// rejected at load with a named error rather than misrouting at runtime.
func TestValidateAgentGroupsRejectsANonFixedPointMember(t *testing.T) {
	cfg := groupCity(heavy())
	normalize := func(_ *City, target string) string {
		if target == "myrig/qwen" {
			return "myrig/luna"
		}
		return target
	}
	err := ValidateAgentGroups(cfg, normalize)
	if err == nil {
		t.Fatal("ValidateAgentGroups accepted a member that normalizes to a different route")
	}
	if !strings.Contains(err.Error(), "would be re-routed") {
		t.Fatalf("error = %q, want it to explain the re-route", err)
	}
	// The identity normalization leaves alone is still accepted.
	cfg.AgentGroups[0].Members = []string{"myrig/luna"}
	if err := ValidateAgentGroups(cfg, normalize); err != nil {
		t.Fatalf("ValidateAgentGroups rejected a fixed-point member: %v", err)
	}
}

func TestAgentGroupDefaults(t *testing.T) {
	g := heavy()
	if got := g.EffectiveStrategy(); got != agentgroup.StrategyOrderedFailover {
		t.Errorf("EffectiveStrategy() = %q, want %q", got, agentgroup.StrategyOrderedFailover)
	}
	g.Strategy = "ordered-failover"
	if got := g.EffectiveStrategy(); got != agentgroup.StrategyOrderedFailover {
		t.Errorf("EffectiveStrategy() = %q, want %q", got, agentgroup.StrategyOrderedFailover)
	}
	if got := (*AgentGroup)(nil).EffectiveStrategy(); got != agentgroup.DefaultStrategy {
		t.Errorf("nil EffectiveStrategy() = %q, want %q", got, agentgroup.DefaultStrategy)
	}
	if got := g.AgentGroupRig(); got != "myrig" {
		t.Errorf("AgentGroupRig() = %q, want %q", got, "myrig")
	}

	var h AgentGroupHealth
	if !h.HonorsDrainAckStranded() || !h.RequiresRoutableRuntime() {
		t.Error("unset health gates must default to on")
	}
	off := false
	h = AgentGroupHealth{DrainAckStranded: &off, RequireRoutableRuntime: &off}
	if h.HonorsDrainAckStranded() || h.RequiresRoutableRuntime() {
		t.Error("explicit false must turn a health gate off")
	}
}

func TestAgentGroupsConfiguredAndFind(t *testing.T) {
	if AgentGroupsConfigured(nil) || AgentGroupsConfigured(groupCity()) {
		t.Error("AgentGroupsConfigured must be false with no groups declared")
	}
	cfg := groupCity(heavy())
	if !AgentGroupsConfigured(cfg) {
		t.Error("AgentGroupsConfigured = false with a group declared")
	}
	if g := FindAgentGroup(cfg, "myrig/heavy"); g == nil || g.Name != "myrig/heavy" {
		t.Errorf("FindAgentGroup = %v, want the declared group", g)
	}
	for _, miss := range []string{"", "myrig/missing", "heavy"} {
		if g := FindAgentGroup(cfg, miss); g != nil {
			t.Errorf("FindAgentGroup(%q) = %v, want nil", miss, g)
		}
	}
	if g := FindAgentGroup(nil, "myrig/heavy"); g != nil {
		t.Errorf("FindAgentGroup(nil) = %v, want nil", g)
	}
}

// The TOML surface is four keys and a two-boolean sub-table, and no more: a
// group must never become a second place to configure an agent.
func TestAgentGroupTOMLRoundTrip(t *testing.T) {
	const src = `
[[agent_groups]]
name = "myrig/heavy"
members = ["myrig/luna", "myrig/qwen"]
strategy = "ordered-failover"

[agent_groups.health]
drain_ack_stranded = false
require_routable_runtime = true
`
	cfg, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cfg.AgentGroups) != 1 {
		t.Fatalf("AgentGroups = %d, want 1", len(cfg.AgentGroups))
	}
	g := cfg.AgentGroups[0]
	if g.Name != "myrig/heavy" || len(g.Members) != 2 || g.Members[0] != "myrig/luna" {
		t.Fatalf("parsed group = %+v", g)
	}
	if g.EffectiveStrategy() != agentgroup.StrategyOrderedFailover {
		t.Errorf("EffectiveStrategy = %q", g.EffectiveStrategy())
	}
	if g.Health.HonorsDrainAckStranded() {
		t.Error("drain_ack_stranded = false did not turn the gate off")
	}
	if !g.Health.RequiresRoutableRuntime() {
		t.Error("require_routable_runtime = true did not stay on")
	}
}
