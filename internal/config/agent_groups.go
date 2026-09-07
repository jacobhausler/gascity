package config

import (
	"fmt"
	"strings"

	"github.com/gastownhall/gascity/internal/agentgroup"
)

// AgentGroup declares a set of interchangeable placement members behind one
// nameable sling target.
//
// A group is the operator's assertion that its members can handle the same
// work. It configures nothing about an agent — a member carries its own
// provider, runtime, model and capacity — so a group never becomes a second
// place to configure an agent. Tiering is expressed by composing different
// groups, not by tagging work.
//
// A group is a placement primitive only. It is resolved to exactly one concrete
// member before anything is written to a bead: gc.routed_to always names a
// single configured agent, exactly as it does today.
type AgentGroup struct {
	// Name is the group's rig-qualified sling target, e.g. "myrig/heavy". It
	// must not collide with a configured agent or named session.
	Name string `toml:"name" jsonschema:"required"`
	// Members lists the group's placement candidates in preference order, as
	// rig-qualified agent identities. Every member must be a configured agent
	// in the group's own rig.
	Members []string `toml:"members" jsonschema:"required"`
	// Strategy selects the placement policy. "" (default) is
	// "ordered-failover", which is the only accepted value today; any other
	// value is rejected at config load.
	Strategy string `toml:"strategy,omitempty" jsonschema:"enum=ordered-failover"`
	// Health gates which members are eligible to be placed on.
	Health AgentGroupHealth `toml:"health,omitempty"`
}

// AgentGroupHealth holds a group's health gates.
//
// Both are pointers because both default to ON: a nil field means "honor this
// signal", and an operator opts out by writing false explicitly. Health gating
// is advisory and self-clearing — a member is skipped while its predicate holds
// and becomes a candidate again as soon as it does not, so no quarantine state
// is persisted and nothing has to be reset by hand.
type AgentGroupHealth struct {
	// DrainAckStranded (default true) skips a member with a fungible seat that
	// acknowledged its own drain while still holding assigned work.
	DrainAckStranded *bool `toml:"drain_ack_stranded,omitempty"`
	// RequireRoutableRuntime (default true) skips a member whose selected
	// runtime_provider cannot be resolved to a declared runtime. This is the
	// only coupling between agent groups and per-agent runtime selection.
	RequireRoutableRuntime *bool `toml:"require_routable_runtime,omitempty"`
}

// HonorsDrainAckStranded reports whether the drain-ack-stranded gate applies.
func (h AgentGroupHealth) HonorsDrainAckStranded() bool {
	return h.DrainAckStranded == nil || *h.DrainAckStranded
}

// RequiresRoutableRuntime reports whether the routable-runtime gate applies.
func (h AgentGroupHealth) RequiresRoutableRuntime() bool {
	return h.RequireRoutableRuntime == nil || *h.RequireRoutableRuntime
}

// EffectiveStrategy returns the group's placement strategy, applying the
// default when none is declared.
func (g *AgentGroup) EffectiveStrategy() agentgroup.Strategy {
	if g == nil {
		return agentgroup.DefaultStrategy
	}
	if s := strings.TrimSpace(g.Strategy); s != "" {
		return agentgroup.Strategy(s)
	}
	return agentgroup.DefaultStrategy
}

// AgentGroupRig returns the rig a group belongs to, derived from its qualified
// name. Group membership is same-rig only, so this is also every member's rig.
func (g *AgentGroup) AgentGroupRig() string {
	if g == nil {
		return ""
	}
	rig, _ := ParseQualifiedName(strings.TrimSpace(g.Name))
	return rig
}

// AgentGroupsConfigured reports whether this city declares any agent group.
// When false every agent-group code path is inert: nothing resolves a group and
// no bead is ever stamped with one.
//
// Sibling of [CityUsesLaneScopedRuntimes], and used for the same purpose: one
// named predicate a caller can check before doing any feature work at all.
func AgentGroupsConfigured(cfg *City) bool {
	return cfg != nil && len(cfg.AgentGroups) > 0
}

// FindAgentGroup returns the group with this exact qualified name, or nil.
func FindAgentGroup(cfg *City, name string) *AgentGroup {
	name = strings.TrimSpace(name)
	if cfg == nil || name == "" {
		return nil
	}
	for i := range cfg.AgentGroups {
		if strings.TrimSpace(cfg.AgentGroups[i].Name) == name {
			return &cfg.AgentGroups[i]
		}
	}
	return nil
}

// ValidateAgentGroups checks every declared group against the city.
//
// normalizeRoute is the caller's route-normalization function (in practice
// agentutil.NormalizePoolRouteTarget), injected because it lives above this
// package. It may be nil, which skips only the fixed-point check.
//
// The rules, and why each exists:
//
//   - The name is rig-qualified and names a configured rig — a group's members
//     must all live in one rig's work store, so the group itself is rig-scoped.
//   - The name does not collide with a configured agent or named session. The
//     resolver puts the group step after the literal-agent steps, so a
//     configured agent would win anyway; rejecting the collision at load makes
//     an ambiguous sling target a config error instead of a surprise.
//   - Members are non-empty, unique, and each is a configured agent. A group
//     that can resolve to nothing is a dead sling target.
//   - No member names another group. Nested groups would make placement
//     recursive for no expressive gain; two groups that share members express
//     the same thing.
//   - Members do not span rigs. gc.routed_to is rig-qualified and demand
//     collection scopes by store, so rebinding a bead onto an agent in another
//     rig would make the row invisible to the new member. Cross-rig placement
//     implies bead relocation and is a genuinely larger design.
//   - The strategy is an accepted value. The enum is single-valued today and
//     must fail closed, not silently accept a later version's name.
//   - Each member's identity is a fixed point of route normalization, so a
//     member named like a pool slot ("<base>-<digits>") can never be collapsed
//     onto a different agent after it is stamped.
func ValidateAgentGroups(cfg *City, normalizeRoute func(*City, string) string) error {
	if cfg == nil || len(cfg.AgentGroups) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(cfg.AgentGroups))
	groupNames := make(map[string]bool, len(cfg.AgentGroups))
	for i := range cfg.AgentGroups {
		groupNames[strings.TrimSpace(cfg.AgentGroups[i].Name)] = true
	}
	for i := range cfg.AgentGroups {
		g := &cfg.AgentGroups[i]
		name := strings.TrimSpace(g.Name)
		if name == "" {
			return fmt.Errorf("agent_groups[%d]: name is required", i)
		}
		if name != g.Name {
			return fmt.Errorf("agent group %q: name must not have surrounding whitespace", g.Name)
		}
		if seen[name] {
			return fmt.Errorf("agent group %q: duplicate name", name)
		}
		seen[name] = true

		rig, local := ParseQualifiedName(name)
		if rig == "" || local == "" {
			return fmt.Errorf("agent group %q: name must be rig-qualified (\"<rig>/<group>\")", name)
		}
		if !hasRigNamed(cfg, rig) {
			return fmt.Errorf("agent group %q: rig %q is not configured", name, rig)
		}
		if FindAgent(cfg, name) != nil {
			return fmt.Errorf("agent group %q: name collides with a configured agent", name)
		}
		if FindNamedSession(cfg, name) != nil {
			return fmt.Errorf("agent group %q: name collides with a configured named session", name)
		}
		if !agentgroup.ValidStrategy(g.Strategy) {
			return fmt.Errorf("agent group %q: strategy must be one of %v or empty, got %q", name, agentgroup.Strategies(), g.Strategy)
		}
		if len(g.Members) == 0 {
			return fmt.Errorf("agent group %q: members is required and must not be empty", name)
		}

		seenMembers := make(map[string]bool, len(g.Members))
		for _, raw := range g.Members {
			member := strings.TrimSpace(raw)
			if member == "" {
				return fmt.Errorf("agent group %q: members must not contain an empty entry", name)
			}
			if member != raw {
				return fmt.Errorf("agent group %q: member %q must not have surrounding whitespace", name, raw)
			}
			if seenMembers[member] {
				return fmt.Errorf("agent group %q: duplicate member %q", name, member)
			}
			seenMembers[member] = true
			if groupNames[member] {
				return fmt.Errorf("agent group %q: member %q is another agent group; groups do not nest", name, member)
			}
			if FindAgent(cfg, member) == nil {
				return fmt.Errorf("agent group %q: member %q is not a configured agent", name, member)
			}
			memberRig, memberLocal := ParseQualifiedName(member)
			if memberRig == "" || memberLocal == "" {
				return fmt.Errorf("agent group %q: member %q must be rig-qualified (\"<rig>/<agent>\")", name, member)
			}
			if memberRig != rig {
				return fmt.Errorf("agent group %q: member %q is in rig %q but the group is in rig %q; members must not span rigs", name, member, memberRig, rig)
			}
			if normalizeRoute != nil {
				if normalized := normalizeRoute(cfg, member); normalized != member {
					return fmt.Errorf("agent group %q: member %q normalizes to route %q, so work routed to it would be re-routed; rename the agent so it is not shaped like a pool slot", name, member, normalized)
				}
			}
		}
	}
	return nil
}
