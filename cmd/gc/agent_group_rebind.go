package main

// Late binding for agent-group work, by REBINDING rather than by deferring.
//
// `gc sling <group>` resolves a group to a concrete member and stamps that
// member in gc.routed_to, plus gc.agent_group as provenance. The alternative —
// persisting the group name and teaching every member to serve it — is the
// design this pass exists to avoid, on two independent grounds:
//
//   - The claim is a raw string compare made in shell, jq and bd flags
//     (cmd_hook_claim.go, the generated Tier-3 work_query), which cannot call
//     into Go. A group name in gc.routed_to is claimable by nobody and countable
//     as demand by nobody — the exact dead-drop NormalizePoolRouteTarget exists
//     to close.
//   - demandServableForTemplates maps a row to the ONE template it is demand
//     for. If N members all served a group route, one bead would become demand
//     for N templates and spawn N seats, N-1 of which read empty and drain —
//     the permanent-demand pathology demand_serve_predicate.go was written to
//     kill.
//
// So the route stays exactly what it is today: one concrete member. What this
// pass adds is that while the bead is still open and unclaimed, the controller
// re-picks that member each tick from the group's strategy and this tick's
// health facts, and persists a different member when the choice changes.
//
// It sits beside collapseSlotSuffixedRoutedWork in the pre-demand phase and
// shares its write discipline: write only on a differing choice (so steady state
// performs no writes and the pass is idempotent), log-and-skip on a write error,
// never block reconciliation. Being here — before demand is counted — is what
// makes a rebound row both countable and claimable in the same tick.

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gastownhall/gascity/internal/agentgroup"
	"github.com/gastownhall/gascity/internal/agentutil"
	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/session"
)

// agentGroupRebindLimitPerTick bounds the durable writes one pass may make, for
// the same reason controlDispatcherRouteRepairLimitPerTick does: each bd/Dolt
// mutation can take seconds, so a large backlog of rows whose member went bad
// must not monopolize a reconciler tick. The rest are rebound on later ticks,
// and until then they keep a route that is still countable and claimable.
const agentGroupRebindLimitPerTick = 5

// agentGroupRebindCursors gives each city its own rotating start offset, so
// neither a persistently unwritable row nor another CityRuntime in the same
// supervisor can starve the tail of the backlog.
var agentGroupRebindCursors sync.Map // map[string]*atomic.Uint64

func agentGroupRebindCursorForDomain(domain string) *atomic.Uint64 {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		domain = "<default>"
	} else {
		domain = filepath.Clean(domain)
	}
	created := &atomic.Uint64{}
	actual, _ := agentGroupRebindCursors.LoadOrStore(domain, created)
	return actual.(*atomic.Uint64)
}

// agentGroupRebindFacts gathers this tick's member-health facts.
//
// Both are supplied rather than discovered inside the policy so the policy stays
// pure and the controller stays the only thing that reads sessions and the
// runtime registry. An absent fact never makes a member INELIGIBLE — a nil
// registry means "no verdict available", not "unroutable".
func agentGroupRebindFacts(cfg *config.City, openSessionInfos []session.Info) agentutil.GroupFacts {
	// Gathering facts is itself feature work: a city with no groups pays
	// nothing, not even the session walk or the registry clone.
	if !config.AgentGroupsConfigured(cfg) {
		return agentutil.GroupFacts{}
	}
	facts := agentutil.GroupFacts{DrainAckStranded: drainAckStrandedTemplates(cfg, openSessionInfos)}
	if reg, err := runtimeRegistryForCity(cfg); err == nil && reg != nil {
		facts.RuntimeResolves = reg.Resolves
	}
	return facts
}

// drainAckStrandedTemplates reports which templates have at least one fungible
// seat that acknowledged its own drain while still holding assigned work.
//
// It applies the same conjunction as exitedDrainAckHolderIdentities, and for the
// same reasons: the marker must be non-empty, the seat must be ASLEEP (the raw
// persisted MetadataState, so a marker surviving a start path that skips
// PreWakePatch cannot make a live seat look dead), and the seat must be fungible
// — a named, configured-named or manual session legitimately returns to its own
// claim. Asleep is not unhealthy; only asleep WITH the marker is.
//
// The signal is taken at face value on the current tick, without a repetition
// threshold: v1 has no failure accumulator. That is tolerable because the
// degradation is self-clearing (the marker is cleared on the next start) and
// ordered-failover returns to the preferred member the moment it does.
func drainAckStrandedTemplates(cfg *config.City, openSessionInfos []session.Info) map[string]bool {
	var stranded map[string]bool
	for _, info := range openSessionInfos {
		if info.Closed || strings.TrimSpace(info.DrainAckStrandedAt) == "" {
			continue
		}
		if strings.TrimSpace(info.MetadataState) != string(session.StateAsleep) {
			continue
		}
		if info.ConfiguredNamedIdentity != "" || info.ConfiguredNamedSession || info.ManualSession {
			continue
		}
		template := strings.TrimSpace(info.Template)
		if template == "" || !findAgentByTemplate(cfg, template).SupportsGenericEphemeralSessions() {
			continue
		}
		if stranded == nil {
			stranded = map[string]bool{}
		}
		stranded[template] = true
	}
	return stranded
}

// rebindAgentGroupRoutedWork re-picks the member for every open, unassigned row
// carrying gc.agent_group whose current member has become ineligible.
//
// Rows it never touches:
//
//   - an ASSIGNED row — the pass filters on an empty assignee, so a running seat
//     can never have its work moved out from under it;
//   - a formula/recipe step — applyAttemptStepRoute stamps no gc.agent_group, so
//     a step resolves its group once and the next ATTEMPT re-resolves it;
//   - a row whose group is gone from config — its concrete route is still valid,
//     and rewriting it would be inventing a placement decision nobody declared;
//   - a row whose group has NO eligible member — failing closed on the last known
//     member keeps the bead countable and claimable, and it recovers on its own
//     as soon as any member becomes eligible.
func rebindAgentGroupRoutedWork(
	rebindDomain string,
	cfg *config.City,
	workBeads []beads.Bead,
	workStores []beads.Store,
	facts agentutil.GroupFacts,
	stderr io.Writer,
) {
	// Structural inertness: a city with no [[agent_groups]] returns here, before
	// reading a single bead.
	if !config.AgentGroupsConfigured(cfg) || len(workBeads) == 0 {
		return
	}
	if len(workBeads) != len(workStores) {
		if stderr != nil {
			fmt.Fprintf(stderr, "rebindAgentGroupRoutedWork: index-aligned input mismatch beads=%d stores=%d\n", len(workBeads), len(workStores)) //nolint:errcheck
		}
		return
	}

	cursor := agentGroupRebindCursorForDomain(rebindDomain)
	start := int((cursor.Add(agentGroupRebindLimitPerTick) - agentGroupRebindLimitPerTick) % uint64(len(workBeads)))
	writesRemaining := agentGroupRebindLimitPerTick
	// Memoized per pass: a backlog is typically many rows over few groups.
	choices := map[string]agentgroup.Choice{}
	noChoice := map[string]bool{}

	for offset := range workBeads {
		if writesRemaining <= 0 {
			return
		}
		i := (start + offset) % len(workBeads)
		wb := workBeads[i]
		if wb.Status != "open" || strings.TrimSpace(wb.Assignee) != "" {
			continue
		}
		store := workStores[i]
		if store == nil {
			continue
		}
		groupName := strings.TrimSpace(wb.Metadata[beadmeta.AgentGroupMetadataKey])
		if groupName == "" || noChoice[groupName] {
			continue
		}
		choice, ok := choices[groupName]
		if !ok {
			g := config.FindAgentGroup(cfg, groupName)
			if g == nil {
				// The group was removed from config. The row's concrete route is
				// still a valid destination, so leave it alone.
				noChoice[groupName] = true
				continue
			}
			choice, ok = agentutil.PickAgentGroupMember(cfg, g, facts)
			if !ok {
				noChoice[groupName] = true
				continue
			}
			choices[groupName] = choice
		}
		if choice.Member == strings.TrimSpace(wb.Metadata[beadmeta.RoutedToMetadataKey]) {
			// Steady state: the persisted member is still the chosen one. This
			// is the common case and it performs no write.
			continue
		}
		writesRemaining--
		opts := beads.UpdateOpts{Metadata: map[string]string{
			beadmeta.RoutedToMetadataKey:           choice.Member,
			beadmeta.AgentGroupStrategyMetadataKey: string(choice.Strategy),
		}}
		if err := store.Update(wb.ID, opts); err != nil {
			if stderr != nil {
				fmt.Fprintf(stderr, "rebindAgentGroupRoutedWork: %s: %v\n", wb.ID, err) //nolint:errcheck
			}
			continue
		}
		if stderr != nil {
			fmt.Fprintf(stderr, "rebindAgentGroupRoutedWork: %s: %s -> %s (%s)\n", wb.ID, wb.Metadata[beadmeta.RoutedToMetadataKey], choice.Member, choice.Reason) //nolint:errcheck
		}
	}
}
