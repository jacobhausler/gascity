package dispatch

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/formula"
)

func agentGroupStepConfig() *config.City {
	maxSessions := 4
	return &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Rigs:      []config.Rig{{Name: "grouprig"}},
		Agents: []config.Agent{
			{Name: "first", Dir: "grouprig", Lifecycle: config.AgentLifecycleOneShot, MaxActiveSessions: &maxSessions},
			{Name: "second", Dir: "grouprig", Lifecycle: config.AgentLifecycleOneShot, MaxActiveSessions: &maxSessions},
		},
		AgentGroups: []config.AgentGroup{{
			Name:    "grouprig/heavy",
			Members: []string{"grouprig/first", "grouprig/second"},
		}},
	}
}

// A formula step may TARGET a group; the dispatcher resolves it once, to a
// concrete member, exactly as if the member had been named directly.
func TestResolveAttemptRouteBindingResolvesAnAgentGroup(t *testing.T) {
	t.Parallel()

	cfg := agentGroupStepConfig()
	store := beads.NewMemStore()

	binding, ok := resolveAttemptRouteBinding("grouprig/heavy", cfg, store)
	if !ok {
		t.Fatal("resolveAttemptRouteBinding did not resolve the group target")
	}
	if binding.qualifiedName != "grouprig/first" {
		t.Fatalf("qualifiedName = %q, want the first eligible member", binding.qualifiedName)
	}
	// A group member is bound exactly as the same agent named directly is.
	direct, ok := resolveAttemptRouteBinding("grouprig/first", cfg, store)
	if !ok {
		t.Fatal("resolveAttemptRouteBinding did not resolve the member directly")
	}
	if binding != direct {
		t.Fatalf("group binding %+v differs from direct binding %+v", binding, direct)
	}

	// Failover applies here too: the first ineligible member is skipped.
	cfg.Agents[0].Suspended = true
	binding, ok = resolveAttemptRouteBinding("grouprig/heavy", cfg, store)
	if !ok || binding.qualifiedName != "grouprig/second" {
		t.Fatalf("binding = %+v, %v; want a failover to grouprig/second", binding, ok)
	}
}

// A configured agent always wins over a group, and an unknown name still fails.
func TestResolveAttemptRouteBindingPrefersAConfiguredAgent(t *testing.T) {
	t.Parallel()

	cfg := agentGroupStepConfig()
	store := beads.NewMemStore()

	binding, ok := resolveAttemptRouteBinding("grouprig/second", cfg, store)
	if !ok || binding.qualifiedName != "grouprig/second" {
		t.Fatalf("binding = %+v, %v; want the literal agent", binding, ok)
	}
	if _, ok := resolveAttemptRouteBinding("grouprig/nobody", cfg, store); ok {
		t.Fatal("resolveAttemptRouteBinding resolved an unknown target")
	}
}

// The load-bearing asymmetry with `gc sling`: a step's route is stamped ONCE
// and carries no gc.agent_group, so no step row is ever eligible for the
// controller's rebind pass. Late binding for a step is the next ATTEMPT
// re-resolving the group, not a second mechanism rewriting the same row.
func TestApplyAttemptStepRouteStampsNoAgentGroup(t *testing.T) {
	t.Parallel()

	cfg := agentGroupStepConfig()
	store := beads.NewMemStore()
	step := &formula.RecipeStep{}

	applyAttemptStepRoute(step, "grouprig/heavy", cfg, store)

	if got := step.Metadata[beadmeta.RoutedToMetadataKey]; got != "grouprig/first" {
		t.Errorf("gc.routed_to = %q, want the resolved member", got)
	}
	if got := step.Metadata[beadmeta.ExecutionRoutedToMetadataKey]; got != "grouprig/first" {
		t.Errorf("gc.execution_routed_to = %q, want the resolved member", got)
	}
	if got, ok := step.Metadata[beadmeta.AgentGroupMetadataKey]; ok {
		t.Errorf("gc.agent_group = %q on a step; a step must never be rebindable", got)
	}
	if got, ok := step.Metadata[beadmeta.AgentGroupStrategyMetadataKey]; ok {
		t.Errorf("gc.agent_group_strategy = %q on a step", got)
	}
}

// With no groups configured the resolver is byte-identical to what it was.
func TestResolveAttemptRouteBindingIsUnchangedWithNoGroups(t *testing.T) {
	t.Parallel()

	cfg := agentGroupStepConfig()
	cfg.AgentGroups = nil
	store := beads.NewMemStore()

	if _, ok := resolveAttemptRouteBinding("grouprig/heavy", cfg, store); ok {
		t.Fatal("a group name resolved in a city that declares no groups")
	}
	binding, ok := resolveAttemptRouteBinding("grouprig/first", cfg, store)
	if !ok || binding.qualifiedName != "grouprig/first" {
		t.Fatalf("binding = %+v, %v; want the ordinary agent binding", binding, ok)
	}
}
