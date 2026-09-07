package main

import (
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/sling"
)

// Resolve-at-sling: a group target resolves to the first eligible member, in
// declared order, as an ordinary agent.
func TestResolveSlingTargetResolvesAnAgentGroup(t *testing.T) {
	cfg := agentGroupConfig()

	a, choice, ok := resolveSlingTarget(cfg, rebindGroup, "")
	if !ok {
		t.Fatal("resolveSlingTarget did not resolve the group")
	}
	if a.QualifiedName() != rebindFirst {
		t.Fatalf("agent = %q, want %q", a.QualifiedName(), rebindFirst)
	}
	if choice.Group != rebindGroup || string(choice.Strategy) != "ordered-failover" {
		t.Fatalf("choice = %+v, want the group and strategy recorded", choice)
	}

	cfg.Agents[0].Suspended = true
	a, choice, ok = resolveSlingTarget(cfg, rebindGroup, "")
	if !ok || a.QualifiedName() != rebindSecond {
		t.Fatalf("agent = %q, %v; want a failover to %q", a.QualifiedName(), ok, rebindSecond)
	}
	if choice.Group != rebindGroup {
		t.Fatalf("choice = %+v, want the group recorded on the failover pick too", choice)
	}
}

// A configured agent always wins, and an ordinary target carries no group
// provenance — so a city with no groups behaves exactly as it does today.
func TestResolveSlingTargetPrefersAConfiguredAgent(t *testing.T) {
	cfg := agentGroupConfig()

	a, choice, ok := resolveSlingTarget(cfg, rebindSecond, "")
	if !ok || a.QualifiedName() != rebindSecond {
		t.Fatalf("agent = %q, %v; want the literal agent", a.QualifiedName(), ok)
	}
	if choice.Group != "" || choice.Member != "" {
		t.Fatalf("choice = %+v, want no group provenance for a direct target", choice)
	}

	cfg.AgentGroups = nil
	if _, _, ok := resolveSlingTarget(cfg, rebindGroup, ""); ok {
		t.Fatal("a group name resolved in a city that declares no groups")
	}
	if a, _, ok := resolveSlingTarget(cfg, rebindFirst, ""); !ok || a.QualifiedName() != rebindFirst {
		t.Fatalf("ordinary resolution changed with no groups: %q, %v", a.QualifiedName(), ok)
	}
}

// A group with no eligible member is a different failure from an unknown name,
// and says which lane to fix.
func TestSlingTargetResolveFailedMsgNamesTheGroup(t *testing.T) {
	cfg := agentGroupConfig()
	cfg.Agents[0].Suspended = true
	cfg.Agents[1].Suspended = true

	msg := slingTargetResolveFailedMsg(cfg, rebindGroup)
	for _, want := range []string{"no eligible member", rebindGroup, rebindFirst, "agent suspended"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
	if got := slingTargetResolveFailedMsg(cfg, "grouprig/nobody"); strings.Contains(got, "no eligible member") {
		t.Errorf("unknown target reported as a group: %q", got)
	}
}

// The stamp is provenance beside the route, never instead of it, and it is a
// no-op for an ordinary target.
func TestStampAgentGroupProvenance(t *testing.T) {
	store := beads.NewMemStore()
	created, err := store.Create(beads.Bead{Title: "work", Type: "task"})
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}

	counting := &countingUpdateStore{Store: store}
	if err := stampAgentGroupProvenance(counting, sling.RouteRequest{BeadID: created.ID}); err != nil {
		t.Fatalf("stampAgentGroupProvenance for an ordinary target: %v", err)
	}
	after, _ := store.Get(created.ID)
	if _, ok := after.Metadata[beadmeta.AgentGroupMetadataKey]; ok {
		t.Error("an ordinary target was stamped with a group")
	}

	err = stampAgentGroupProvenance(store, sling.RouteRequest{
		BeadID:             created.ID,
		Target:             rebindFirst,
		AgentGroup:         rebindGroup,
		AgentGroupStrategy: "ordered-failover",
	})
	if err != nil {
		t.Fatalf("stampAgentGroupProvenance: %v", err)
	}
	after, _ = store.Get(created.ID)
	if got := after.Metadata[beadmeta.AgentGroupMetadataKey]; got != rebindGroup {
		t.Errorf("gc.agent_group = %q, want %q", got, rebindGroup)
	}
	if got := after.Metadata[beadmeta.AgentGroupStrategyMetadataKey]; got != "ordered-failover" {
		t.Errorf("gc.agent_group_strategy = %q, want ordered-failover", got)
	}
}
