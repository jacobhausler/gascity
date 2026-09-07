package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/agentutil"
	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/session"
)

const (
	rebindGroup   = "grouprig/heavy"
	rebindFirst   = "grouprig/first"
	rebindSecond  = "grouprig/second"
	rebindDomainT = "/tmp/group-city"
)

func agentGroupConfig() *config.City {
	max := 4
	return &config.City{
		Rigs: []config.Rig{{Name: "grouprig"}},
		Agents: []config.Agent{
			{Name: "first", Dir: "grouprig", Lifecycle: config.AgentLifecycleOneShot, MaxActiveSessions: &max},
			{Name: "second", Dir: "grouprig", Lifecycle: config.AgentLifecycleOneShot, MaxActiveSessions: &max},
		},
		AgentGroups: []config.AgentGroup{{
			Name:    rebindGroup,
			Members: []string{rebindFirst, rebindSecond},
		}},
	}
}

// seedGroupRow creates one open work bead already routed at rebindFirst under
// the group, the shape `gc sling <group>` produces.
func seedGroupRow(t *testing.T, store beads.Store, metadata map[string]string, assignee string) beads.Bead {
	t.Helper()
	md := map[string]string{
		beadmeta.RoutedToMetadataKey:   rebindFirst,
		beadmeta.AgentGroupMetadataKey: rebindGroup,
	}
	for k, v := range metadata {
		md[k] = v
	}
	created, err := store.Create(beads.Bead{Title: "group work", Type: "task", Metadata: md})
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if assignee != "" {
		if err := store.Update(created.ID, beads.UpdateOpts{Assignee: &assignee}); err != nil {
			t.Fatalf("assigning: %v", err)
		}
		created, _ = store.Get(created.ID)
	}
	return created
}

func routeOf(t *testing.T, store beads.Store, id string) string {
	t.Helper()
	got, err := store.Get(id)
	if err != nil {
		t.Fatalf("re-reading %s: %v", id, err)
	}
	return strings.TrimSpace(got.Metadata[beadmeta.RoutedToMetadataKey])
}

func runRebind(cfg *config.City, row beads.Bead, store beads.Store, facts agentutil.GroupFacts) *bytes.Buffer {
	var stderr bytes.Buffer
	rebindAgentGroupRoutedWork(rebindDomainT, cfg, []beads.Bead{row}, []beads.Store{store}, facts, &stderr)
	return &stderr
}

// RED-to-GREEN: each way the preferred member can become ineligible moves the
// unclaimed row to the next member.
func TestRebindMovesAnUnclaimedRowWhenItsMemberGoesIneligible(t *testing.T) {
	zero := 0
	tests := []struct {
		name   string
		mutate func(*config.City)
		facts  agentutil.GroupFacts
	}{
		{
			name:   "agent suspended",
			mutate: func(c *config.City) { c.Agents[0].Suspended = true },
		},
		{
			name:   "rig suspended",
			mutate: func(c *config.City) { c.Rigs[0].Suspended = true },
		},
		{
			name:   "max_active_sessions = 0",
			mutate: func(c *config.City) { c.Agents[0].MaxActiveSessions = &zero },
		},
		{
			name:  "a seat drain-acked while holding work",
			facts: agentutil.GroupFacts{DrainAckStranded: map[string]bool{rebindFirst: true}},
		},
		{
			name:   "runtime is not resolvable",
			mutate: func(c *config.City) { c.Agents[0].RuntimeProvider = "ssh:gone@nowhere" },
			facts:  agentutil.GroupFacts{RuntimeResolves: func(string) bool { return false }},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := agentGroupConfig()
			store := beads.NewMemStore()
			row := seedGroupRow(t, store, nil, "")

			// Before: nothing is wrong, so nothing moves. The degradation is
			// applied only after this baseline, whether it lives in config or
			// in the facts.
			runRebind(cfg, row, store, agentutil.GroupFacts{})
			if got := routeOf(t, store, row.ID); got != rebindFirst {
				t.Fatalf("route before degradation = %q, want %q", got, rebindFirst)
			}

			if tc.mutate != nil {
				tc.mutate(cfg)
			}
			if tc.name == "rig suspended" {
				// A suspended rig makes EVERY member ineligible, so the pass must
				// leave the route alone rather than invent a placement.
				runRebind(cfg, row, store, tc.facts)
				if got := routeOf(t, store, row.ID); got != rebindFirst {
					t.Fatalf("route with no eligible member = %q, want it left at %q", got, rebindFirst)
				}
				return
			}
			runRebind(cfg, row, store, tc.facts)
			if got := routeOf(t, store, row.ID); got != rebindSecond {
				t.Fatalf("route after degradation = %q, want %q", got, rebindSecond)
			}
			// The strategy that made the new pick is recorded alongside it.
			after, _ := store.Get(row.ID)
			if got := after.Metadata[beadmeta.AgentGroupStrategyMetadataKey]; got != "ordered-failover" {
				t.Errorf("recorded strategy = %q, want ordered-failover", got)
			}
			// The group stamp survives, so the row stays rebindable.
			if got := after.Metadata[beadmeta.AgentGroupMetadataKey]; got != rebindGroup {
				t.Errorf("group stamp = %q, want %q", got, rebindGroup)
			}
		})
	}
}

// A running seat must never have its work moved out from under it.
func TestRebindNeverTouchesAnAssignedRow(t *testing.T) {
	cfg := agentGroupConfig()
	cfg.Agents[0].Suspended = true
	store := beads.NewMemStore()
	row := seedGroupRow(t, store, nil, "grouprig/first-1")

	counting := &countingUpdateStore{Store: store}
	var stderr bytes.Buffer
	rebindAgentGroupRoutedWork(rebindDomainT, cfg, []beads.Bead{row}, []beads.Store{counting}, agentutil.GroupFacts{}, &stderr)
	if counting.updates != 0 {
		t.Fatalf("updates on an assigned row = %d, want 0", counting.updates)
	}
	if got := routeOf(t, store, row.ID); got != rebindFirst {
		t.Fatalf("route = %q, want the assigned row left at %q", got, rebindFirst)
	}
}

// A formula/recipe step carries no gc.agent_group, so it is structurally
// excluded: a step resolves its group once and the next attempt re-resolves it.
func TestRebindNeverTouchesARowWithoutTheGroupStamp(t *testing.T) {
	cfg := agentGroupConfig()
	cfg.Agents[0].Suspended = true
	store := beads.NewMemStore()
	created, err := store.Create(beads.Bead{
		Title: "formula step", Type: "task",
		Metadata: map[string]string{
			beadmeta.RoutedToMetadataKey:          rebindFirst,
			beadmeta.ExecutionRoutedToMetadataKey: rebindFirst,
		},
	})
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}

	counting := &countingUpdateStore{Store: store}
	var stderr bytes.Buffer
	rebindAgentGroupRoutedWork(rebindDomainT, cfg, []beads.Bead{created}, []beads.Store{counting}, agentutil.GroupFacts{}, &stderr)
	if counting.updates != 0 {
		t.Fatalf("updates on a step row = %d, want 0", counting.updates)
	}
	after, _ := store.Get(created.ID)
	if got := after.Metadata[beadmeta.RoutedToMetadataKey]; got != rebindFirst {
		t.Errorf("gc.routed_to = %q, want %q", got, rebindFirst)
	}
	if got := after.Metadata[beadmeta.ExecutionRoutedToMetadataKey]; got != rebindFirst {
		t.Errorf("gc.execution_routed_to = %q, want it untouched", got)
	}
}

// Steady state performs no writes, so the pass cannot become a per-tick write
// amplifier on the open-routed backlog. Same property collapseSlotSuffixedRouted
// Work is built on.
func TestRebindIsIdempotent(t *testing.T) {
	cfg := agentGroupConfig()
	cfg.Agents[0].Suspended = true
	store := beads.NewMemStore()
	row := seedGroupRow(t, store, nil, "")

	counting := &countingUpdateStore{Store: store}
	var stderr bytes.Buffer
	rebindAgentGroupRoutedWork(rebindDomainT, cfg, []beads.Bead{row}, []beads.Store{counting}, agentutil.GroupFacts{}, &stderr)
	if counting.updates != 1 {
		t.Fatalf("updates on the first pass = %d, want 1", counting.updates)
	}
	rebound, _ := store.Get(row.ID)
	rebindAgentGroupRoutedWork(rebindDomainT, cfg, []beads.Bead{rebound}, []beads.Store{counting}, agentutil.GroupFacts{}, &stderr)
	if counting.updates != 1 {
		t.Fatalf("updates once the route already names the chosen member = %d, want no second write", counting.updates)
	}
}

// A group deleted from config leaves its beads alone: the concrete route they
// carry is still a valid destination.
func TestRebindLeavesRowsAloneWhenTheGroupIsGone(t *testing.T) {
	cfg := agentGroupConfig()
	store := beads.NewMemStore()
	row := seedGroupRow(t, store, map[string]string{beadmeta.AgentGroupMetadataKey: "grouprig/retired"}, "")

	counting := &countingUpdateStore{Store: store}
	var stderr bytes.Buffer
	rebindAgentGroupRoutedWork(rebindDomainT, cfg, []beads.Bead{row}, []beads.Store{counting}, agentutil.GroupFacts{}, &stderr)
	if counting.updates != 0 {
		t.Fatalf("updates for a retired group = %d, want 0", counting.updates)
	}
	if got := routeOf(t, store, row.ID); got != rebindFirst {
		t.Fatalf("route = %q, want %q", got, rebindFirst)
	}
}

// A city with no [[agent_groups]] never enters the pass at all.
func TestRebindIsInertWithNoGroupsConfigured(t *testing.T) {
	cfg := agentGroupConfig()
	cfg.AgentGroups = nil
	store := beads.NewMemStore()
	row := seedGroupRow(t, store, nil, "")

	counting := &countingUpdateStore{Store: store}
	var stderr bytes.Buffer
	rebindAgentGroupRoutedWork(rebindDomainT, cfg, []beads.Bead{row}, []beads.Store{counting}, agentGroupRebindFacts(cfg, nil), &stderr)
	if counting.updates != 0 {
		t.Fatalf("updates with no groups configured = %d, want 0", counting.updates)
	}
	if stderr.Len() != 0 {
		t.Fatalf("inert pass wrote to stderr: %s", stderr.String())
	}
	// The fact-gathering half is inert too: no session walk, no registry.
	if facts := agentGroupRebindFacts(cfg, nil); facts.DrainAckStranded != nil || facts.RuntimeResolves != nil {
		t.Errorf("agentGroupRebindFacts gathered facts for a groupless city: %+v", facts)
	}
}

// The per-tick write budget caps the blast radius; the rotating cursor means the
// rows it did not reach are the ones a later tick starts from.
func TestRebindBudgetCapsWritesAndTheCursorRotates(t *testing.T) {
	cfg := agentGroupConfig()
	cfg.Agents[0].Suspended = true
	store := beads.NewMemStore()

	total := agentGroupRebindLimitPerTick * 3
	rows := make([]beads.Bead, 0, total)
	stores := make([]beads.Store, 0, total)
	counting := &countingUpdateStore{Store: store}
	for i := 0; i < total; i++ {
		rows = append(rows, seedGroupRow(t, store, nil, ""))
		stores = append(stores, counting)
	}

	var stderr bytes.Buffer
	rebindAgentGroupRoutedWork(rebindDomainT+"/budget", cfg, rows, stores, agentutil.GroupFacts{}, &stderr)
	if counting.updates != agentGroupRebindLimitPerTick {
		t.Fatalf("updates in one pass = %d, want the budget %d", counting.updates, agentGroupRebindLimitPerTick)
	}

	rebound := 0
	for _, row := range rows {
		if routeOf(t, store, row.ID) == rebindSecond {
			rebound++
		}
	}
	if rebound != agentGroupRebindLimitPerTick {
		t.Fatalf("rebound rows = %d, want %d", rebound, agentGroupRebindLimitPerTick)
	}

	// Later ticks drain the tail rather than re-writing the head.
	for tick := 0; tick < 3; tick++ {
		fresh := make([]beads.Bead, 0, total)
		for _, row := range rows {
			got, err := store.Get(row.ID)
			if err != nil {
				t.Fatalf("re-reading: %v", err)
			}
			fresh = append(fresh, got)
		}
		rebindAgentGroupRoutedWork(rebindDomainT+"/budget", cfg, fresh, stores, agentutil.GroupFacts{}, &stderr)
	}
	for _, row := range rows {
		if got := routeOf(t, store, row.ID); got != rebindSecond {
			t.Fatalf("row %s route = %q after draining ticks, want %q", row.ID, got, rebindSecond)
		}
	}
}

// The drain-ack fact reuses the fix's own conjunction: only an ASLEEP, fungible
// seat carrying a non-empty marker counts. Asleep alone is not unhealthy.
func TestDrainAckStrandedTemplates(t *testing.T) {
	cfg := agentGroupConfig()
	asleep := string(session.StateAsleep)

	stranded := session.Info{Template: rebindFirst, DrainAckStrandedAt: "2026-08-28T00:00:00Z", MetadataState: asleep}
	if got := drainAckStrandedTemplates(cfg, []session.Info{stranded}); !got[rebindFirst] {
		t.Errorf("a stranded asleep fungible seat was not reported: %v", got)
	}

	negatives := []struct {
		name string
		info session.Info
	}{
		{"asleep without the marker", session.Info{Template: rebindFirst, MetadataState: asleep}},
		{"marker but not asleep", session.Info{Template: rebindFirst, DrainAckStrandedAt: "t", MetadataState: "running"}},
		{"closed", session.Info{Template: rebindFirst, DrainAckStrandedAt: "t", MetadataState: asleep, Closed: true}},
		{"manual session", session.Info{Template: rebindFirst, DrainAckStrandedAt: "t", MetadataState: asleep, ManualSession: true}},
		{"configured named session", session.Info{Template: rebindFirst, DrainAckStrandedAt: "t", MetadataState: asleep, ConfiguredNamedSession: true}},
		{"unknown template", session.Info{Template: "grouprig/ghost", DrainAckStrandedAt: "t", MetadataState: asleep}},
	}
	for _, tc := range negatives {
		t.Run(tc.name, func(t *testing.T) {
			if got := drainAckStrandedTemplates(cfg, []session.Info{tc.info}); len(got) != 0 {
				t.Errorf("reported stranded: %v", got)
			}
		})
	}
}

// The same-tick property the pre-demand placement of this pass exists to
// guarantee: after the rebind, the row is capacity demand for the NEW member,
// and the claim predicate serves it to that same member. Both halves of the
// agreement invariant move together, because the route is still exactly one
// concrete template — the thing that would break if a group name were ever
// persisted in gc.routed_to.
func TestRebindKeepsDemandAndClaimInAgreementOnTheNewMember(t *testing.T) {
	cfg := agentGroupConfig()
	cfg.Agents[0].Suspended = true
	store := beads.NewMemStore()
	row := seedGroupRow(t, store, nil, "")

	templates := map[string]struct{}{rebindFirst: {}, rebindSecond: {}}
	before, _ := store.Get(row.ID)
	if got, ok := demandServableForTemplates(cfg, before, templates); !ok || got != rebindFirst {
		t.Fatalf("demand before rebind = %q, %v; want %q", got, ok, rebindFirst)
	}

	var stderr bytes.Buffer
	rebindAgentGroupRoutedWork(rebindDomainT, cfg, []beads.Bead{before}, []beads.Store{store}, agentutil.GroupFacts{}, &stderr)

	after, _ := store.Get(row.ID)
	got, ok := demandServableForTemplates(cfg, after, templates)
	if !ok || got != rebindSecond {
		t.Fatalf("demand after rebind = %q, %v; want %q in the same tick", got, ok, rebindSecond)
	}
	if !hookClaimMatchesRoute(after, []string{rebindSecond}) {
		t.Error("the new member's claim predicate does not serve the rebound row")
	}
	if hookClaimMatchesRoute(after, []string{rebindFirst}) {
		t.Error("the old member's claim predicate still serves the rebound row")
	}
	// And never the group name: a route naming a group is claimable by nobody.
	if hookClaimMatchesRoute(after, []string{rebindGroup}) {
		t.Error("the group name matched the claim predicate")
	}
}
