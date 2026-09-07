package agentutil

import (
	"testing"

	"github.com/gastownhall/gascity/internal/agentgroup"
	"github.com/gastownhall/gascity/internal/config"
)

func groupTestCity() *config.City {
	return &config.City{
		Rigs: []config.Rig{{Name: "myrig"}, {Name: "sleepy", Suspended: true}},
		Agents: []config.Agent{
			{Name: "luna", Dir: "myrig", Lifecycle: config.AgentLifecycleOneShot, MaxActiveSessions: intPtr(8)},
			{Name: "qwen", Dir: "myrig", Lifecycle: config.AgentLifecycleOneShot, MaxActiveSessions: intPtr(2)},
			{Name: "sol", Dir: "myrig", Lifecycle: config.AgentLifecycleOneShot, MaxActiveSessions: intPtr(3)},
			{Name: "napper", Dir: "sleepy", MaxActiveSessions: intPtr(2)},
		},
		AgentGroups: []config.AgentGroup{{
			Name:    "myrig/heavy",
			Members: []string{"myrig/luna", "myrig/qwen", "myrig/sol"},
		}},
	}
}

func mustPick(t *testing.T, cfg *config.City, facts GroupFacts) agentgroup.Choice {
	t.Helper()
	choice, ok := PickAgentGroupMember(cfg, config.FindAgentGroup(cfg, "myrig/heavy"), facts)
	if !ok {
		t.Fatal("PickAgentGroupMember made no choice")
	}
	return choice
}

func TestPickAgentGroupMemberPrefersTheFirstMember(t *testing.T) {
	cfg := groupTestCity()
	if got := mustPick(t, cfg, GroupFacts{}).Member; got != "myrig/luna" {
		t.Fatalf("Member = %q, want myrig/luna", got)
	}
}

func TestPickAgentGroupMemberSkipsIneligibleMembers(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.City)
		facts  GroupFacts
		want   string
	}{
		{
			name:   "suspended agent",
			mutate: func(c *config.City) { c.Agents[0].Suspended = true },
			want:   "myrig/qwen",
		},
		{
			name:   "zero capacity",
			mutate: func(c *config.City) { c.Agents[0].MaxActiveSessions = intPtr(0) },
			want:   "myrig/qwen",
		},
		{
			name:   "suspended rig",
			mutate: func(c *config.City) { c.AgentGroups[0].Members = []string{"sleepy/napper", "myrig/qwen"} },
			want:   "myrig/qwen",
		},
		{
			name:   "unknown member",
			mutate: func(c *config.City) { c.AgentGroups[0].Members = []string{"myrig/ghost", "myrig/qwen"} },
			want:   "myrig/qwen",
		},
		{
			name:  "drain-ack stranded holder",
			facts: GroupFacts{DrainAckStranded: map[string]bool{"myrig/luna": true}},
			want:  "myrig/qwen",
		},
		{
			name: "unroutable runtime",
			mutate: func(c *config.City) {
				c.Agents[0].RuntimeProvider = "ssh:gone@nowhere"
			},
			facts: GroupFacts{RuntimeResolves: func(string) bool { return false }},
			want:  "myrig/qwen",
		},
		{
			name: "two ineligible members fall through to the third",
			mutate: func(c *config.City) {
				c.Agents[0].Suspended = true
				c.Agents[1].MaxActiveSessions = intPtr(0)
			},
			want: "myrig/sol",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := groupTestCity()
			if tc.mutate != nil {
				tc.mutate(cfg)
			}
			if got := mustPick(t, cfg, tc.facts).Member; got != tc.want {
				t.Fatalf("Member = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPickAgentGroupMemberHealthGatesHonorTheirOptOut(t *testing.T) {
	off := false

	cfg := groupTestCity()
	cfg.AgentGroups[0].Health.DrainAckStranded = &off
	facts := GroupFacts{DrainAckStranded: map[string]bool{"myrig/luna": true}}
	if got := mustPick(t, cfg, facts).Member; got != "myrig/luna" {
		t.Errorf("drain_ack_stranded = false: Member = %q, want myrig/luna", got)
	}

	cfg = groupTestCity()
	cfg.Agents[0].RuntimeProvider = "ssh:gone@nowhere"
	cfg.AgentGroups[0].Health.RequireRoutableRuntime = &off
	facts = GroupFacts{RuntimeResolves: func(string) bool { return false }}
	if got := mustPick(t, cfg, facts).Member; got != "myrig/luna" {
		t.Errorf("require_routable_runtime = false: Member = %q, want myrig/luna", got)
	}
}

// A member on the city-default runtime selects nothing lane-scoped, so it is
// always routable — otherwise the gate would make every member of every
// ordinary city ineligible.
func TestRoutableRuntimeGateIgnoresTheCityDefault(t *testing.T) {
	cfg := groupTestCity()
	facts := GroupFacts{RuntimeResolves: func(string) bool { return false }}
	if got := mustPick(t, cfg, facts).Member; got != "myrig/luna" {
		t.Fatalf("Member = %q, want myrig/luna", got)
	}
}

// The gate reads only the lane-scoped selection. A city-level [session]
// provider the registry cannot resolve must not strand every member of every
// group — direct slings to those same members keep working.
func TestRoutableRuntimeGateIgnoresAnUnresolvableCityProvider(t *testing.T) {
	cfg := groupTestCity()
	cfg.Session.Provider = "ssh:gone@nowhere"
	facts := GroupFacts{RuntimeResolves: func(string) bool { return false }}
	if got := mustPick(t, cfg, facts).Member; got != "myrig/luna" {
		t.Fatalf("Member = %q, want myrig/luna", got)
	}

	// The same member with its own unresolvable lane-scoped override is still
	// ineligible, city provider or not.
	cfg.Agents[0].RuntimeProvider = "ssh:also-gone@nowhere"
	if got := mustPick(t, cfg, facts).Member; got != "myrig/qwen" {
		t.Fatalf("lane-scoped override: Member = %q, want myrig/qwen", got)
	}
}

// No registry in hand is not a verdict of unroutable.
func TestRoutableRuntimeGateIsInertWithoutFacts(t *testing.T) {
	cfg := groupTestCity()
	cfg.Agents[0].RuntimeProvider = "ssh:gone@nowhere"
	if got := mustPick(t, cfg, GroupFacts{}).Member; got != "myrig/luna" {
		t.Fatalf("Member = %q, want myrig/luna", got)
	}
}

func TestPickAgentGroupMemberMakesNoChoiceWhenNoMemberIsEligible(t *testing.T) {
	cfg := groupTestCity()
	for i := range cfg.Agents {
		cfg.Agents[i].Suspended = true
	}
	if choice, ok := PickAgentGroupMember(cfg, config.FindAgentGroup(cfg, "myrig/heavy"), GroupFacts{}); ok {
		t.Fatalf("PickAgentGroupMember = %+v, want no choice", choice)
	}
	if _, ok := PickAgentGroupMember(cfg, nil, GroupFacts{}); ok {
		t.Fatal("PickAgentGroupMember(nil group) made a choice")
	}
}

func TestResolveAgentGroupTarget(t *testing.T) {
	cfg := groupTestCity()
	a, choice, ok := ResolveAgentGroupTarget(cfg, "myrig/heavy", GroupFacts{})
	if !ok {
		t.Fatal("ResolveAgentGroupTarget did not resolve the group")
	}
	if a.QualifiedName() != "myrig/luna" {
		t.Errorf("agent = %q, want myrig/luna", a.QualifiedName())
	}
	// The resolved agent must flow through the ordinary routing derivation
	// unchanged: what is stamped is one concrete template, never the group.
	if got := RoutedToIdentity(&a); got != "myrig/luna" {
		t.Errorf("RoutedToIdentity = %q, want myrig/luna", got)
	}
	if got := NormalizePoolRouteTarget(cfg, RoutedToIdentity(&a)); got != "myrig/luna" {
		t.Errorf("NormalizePoolRouteTarget = %q, want myrig/luna", got)
	}
	if choice.Group != "myrig/heavy" || choice.Strategy != agentgroup.StrategyOrderedFailover {
		t.Errorf("choice = %+v, want the group and strategy recorded", choice)
	}
}

func TestResolveAgentGroupTargetDeclinesNonGroups(t *testing.T) {
	cfg := groupTestCity()
	for _, input := range []string{"", "myrig/luna", "heavy", "myrig/missing"} {
		if _, _, ok := ResolveAgentGroupTarget(cfg, input, GroupFacts{}); ok {
			t.Errorf("ResolveAgentGroupTarget(%q) resolved; want ok=false", input)
		}
	}
	// A city with no groups declared never enters the feature at all.
	bare := groupTestCity()
	bare.AgentGroups = nil
	if _, _, ok := ResolveAgentGroupTarget(bare, "myrig/heavy", GroupFacts{}); ok {
		t.Error("ResolveAgentGroupTarget resolved with no groups configured")
	}
	if _, _, ok := ResolveAgentGroupTarget(nil, "myrig/heavy", GroupFacts{}); ok {
		t.Error("ResolveAgentGroupTarget(nil cfg) resolved")
	}
}

func TestValidateAgentGroupsAppliesRouteNormalization(t *testing.T) {
	cfg := groupTestCity()
	if err := ValidateAgentGroups(cfg); err != nil {
		t.Fatalf("ValidateAgentGroups: %v", err)
	}
	// A member shaped like a live slot of an unbounded pool would be collapsed
	// onto that pool's base after it was stamped. 5181c95ab makes a configured
	// agent win over the slot shape, so this stays valid — the fixed-point
	// check is the belt-and-braces guard for any shape that fix does not cover.
	cfg.Agents = append(cfg.Agents, config.Agent{Name: "pool", Dir: "myrig", Lifecycle: config.AgentLifecycleOneShot})
	cfg.Agents = append(cfg.Agents, config.Agent{Name: "pool-4090", Dir: "myrig", Lifecycle: config.AgentLifecycleOneShot, MaxActiveSessions: intPtr(2)})
	cfg.AgentGroups[0].Members = append(cfg.AgentGroups[0].Members, "myrig/pool-4090")
	if err := ValidateAgentGroups(cfg); err != nil {
		t.Fatalf("ValidateAgentGroups rejected a configured agent shaped like a slot: %v", err)
	}
}
