package agentutil

import (
	"strings"

	"github.com/gastownhall/gascity/internal/agentgroup"
	"github.com/gastownhall/gascity/internal/config"
)

// GroupFacts carries the per-tick health facts the member-eligibility filter
// reads beyond the configuration itself.
//
// Both fields are optional, and a zero GroupFacts is the honest answer for a
// caller that holds no such facts — a `gc sling` process has no session
// snapshot and no runtime registry, so it filters on configuration alone and
// leaves the rest to the controller's rebind pass, which does hold them.
// Nothing here ever makes a member eligible; the facts can only remove one.
type GroupFacts struct {
	// DrainAckStranded reports, per member route identity, whether a fungible
	// seat of that member acknowledged its own drain while still holding
	// assigned work — the signal that the member's seats are silently
	// stranding beads.
	DrainAckStranded map[string]bool

	// RuntimeResolves reports whether a runtime selection name can be
	// constructed by this process. nil means "no registry in hand", which is
	// read as routable: a caller that cannot tell must not invent an
	// ineligibility. It is consulted only for a member that selects a
	// lane-scoped runtime — its own runtime_provider or its rig's default; a
	// member that selects none, and so keeps the city-wide [session] provider,
	// is always routable here.
	RuntimeResolves func(runtimeName string) bool
}

// AgentGroupMembers returns the group's members in declared order, each with
// this moment's eligibility verdict, ready for agentgroup.Pick.
//
// v1 eligibility is: the member must be a configured agent, must not be
// suspended, must not sit in a suspended rig, must not have max_active_sessions
// = 0, and — under the group's health gates — must not be stranding work or
// selecting an unresolvable runtime.
//
// Note what is deliberately absent: a member is never skipped for being merely
// BUSY. Free capacity would need the busy-seat count that the controller's
// pre-demand phase has not read yet, and routing to a saturated pool is
// harmless — the bead waits in that pool's queue and the pull system serves it
// when a seat frees. Only ineligible members are skipped.
func AgentGroupMembers(cfg *config.City, g *config.AgentGroup, facts GroupFacts) []agentgroup.Member {
	if cfg == nil || g == nil || len(g.Members) == 0 {
		return nil
	}
	members := make([]agentgroup.Member, 0, len(g.Members))
	for _, raw := range g.Members {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		reason := memberIneligibleReason(cfg, g, name, facts)
		members = append(members, agentgroup.Member{
			Name:     name,
			Eligible: reason == "",
			Reason:   reason,
		})
	}
	return members
}

// memberIneligibleReason returns why a member cannot be placed on, or "" when
// it is eligible.
func memberIneligibleReason(cfg *config.City, g *config.AgentGroup, member string, facts GroupFacts) string {
	a, ok := ResolveAgent(cfg, member, ResolveOpts{TemplateOnly: true})
	if !ok {
		return "not a configured agent"
	}
	if a.Suspended {
		return "agent suspended"
	}
	if rigSuspended(cfg, a.OwningRig()) {
		return "rig suspended"
	}
	if ScaleParamsFor(&a).Max == 0 {
		return "max_active_sessions = 0"
	}
	if g.Health.HonorsDrainAckStranded() && facts.DrainAckStranded[member] {
		return "a seat drain-acked while holding assigned work"
	}
	if g.Health.RequiresRoutableRuntime() && facts.RuntimeResolves != nil {
		// Only a *lane-scoped* selection — the member's own runtime_provider or
		// its rig's default — can make this member unroutable, so the check
		// consults AgentRuntimeProviderOverride and never the full resolution
		// order. An empty override means the member keeps the city-wide
		// behavior: whatever the city-level [session] provider is, it is the
		// same runtime every direct sling to that member already uses, so it is
		// routable by construction here. Resolving the city default instead
		// would let one unresolvable city-wide name make every member of every
		// group ineligible while direct slings still work.
		if rt := config.AgentRuntimeProviderOverrideValue(cfg, &a); rt != "" && !facts.RuntimeResolves(rt) {
			return "runtime " + rt + " cannot be resolved"
		}
	}
	return ""
}

// rigSuspended reports whether a rig is suspended in configuration. Mirrors the
// same check the sling preflight makes before warning that a routed bead may
// never be picked up.
func rigSuspended(cfg *config.City, rigName string) bool {
	if cfg == nil || rigName == "" {
		return false
	}
	for i := range cfg.Rigs {
		if cfg.Rigs[i].Name == rigName {
			return cfg.Rigs[i].Suspended
		}
	}
	return false
}

// PickAgentGroupMember resolves a group to one concrete member for this moment,
// or ok=false when no member is eligible.
func PickAgentGroupMember(cfg *config.City, g *config.AgentGroup, facts GroupFacts) (agentgroup.Choice, bool) {
	if g == nil {
		return agentgroup.Choice{}, false
	}
	return agentgroup.Pick(strings.TrimSpace(g.Name), g.EffectiveStrategy(), AgentGroupMembers(cfg, g, facts))
}

// ResolveAgentGroupTarget resolves a sling target that names an agent group to
// the chosen member's config.Agent, plus the choice's provenance.
//
// It returns ok=false when the input names no group (the overwhelmingly common
// case, and the reason every caller can put this behind one cheap check) or
// when the group has no eligible member. Callers must treat the returned agent
// exactly as if the operator had typed the member's own name: everything
// downstream — RoutedToIdentity, NormalizePoolRouteTarget, the demand
// predicate, the claim predicate — then sees one concrete template, unchanged.
func ResolveAgentGroupTarget(cfg *config.City, input string, facts GroupFacts) (config.Agent, agentgroup.Choice, bool) {
	if !config.AgentGroupsConfigured(cfg) {
		return config.Agent{}, agentgroup.Choice{}, false
	}
	g := config.FindAgentGroup(cfg, strings.TrimSpace(input))
	if g == nil {
		return config.Agent{}, agentgroup.Choice{}, false
	}
	choice, ok := PickAgentGroupMember(cfg, g, facts)
	if !ok {
		return config.Agent{}, agentgroup.Choice{}, false
	}
	a, ok := ResolveAgent(cfg, choice.Member, ResolveOpts{TemplateOnly: true})
	if !ok {
		return config.Agent{}, agentgroup.Choice{}, false
	}
	return a, choice, true
}

// ValidateAgentGroups validates a city's agent groups, supplying the route
// normalization this package owns. It is the form every caller outside
// internal/config should use.
func ValidateAgentGroups(cfg *config.City) error {
	return config.ValidateAgentGroups(cfg, NormalizePoolRouteTarget)
}
