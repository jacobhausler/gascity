// Package agentgroup holds the placement policy for agent groups: given a
// group's members in declared order and this moment's eligibility verdict for
// each, which single member should the work be routed to.
//
// It is pure — no config, no store, no clock, no I/O — mirroring
// internal/poolplan. The caller gathers the facts and applies the verdict; this
// package only makes the choice, so the choice is testable without a city.
//
// The choice is always ONE concrete member. A group name is never a routing
// destination: gc's pool is a pull system whose claim predicate compares
// gc.routed_to to a template name as a raw string in shell/jq/bd flags, so a
// route naming a group would be claimable by nobody and countable as demand by
// nobody (see cmd/gc/demand_serve_predicate.go).
package agentgroup

import (
	"fmt"
	"strings"
)

// Strategy names a placement policy.
//
// The type is an enum with a single value today. It exists as an enum rather
// than as a bare bool or an implicit default so that adding a load- or
// failure-aware policy later is an enum extension, not a config-shape change,
// and so a resolved choice records which policy actually made the pick.
type Strategy string

const (
	// StrategyOrderedFailover prefers the first eligible member in declared
	// order. It is deterministic, needs no load or failure input, and returns
	// to the preferred member as soon as that member is eligible again — so it
	// cannot oscillate.
	StrategyOrderedFailover Strategy = "ordered-failover"
)

// DefaultStrategy is the strategy applied when a group declares none.
const DefaultStrategy = StrategyOrderedFailover

// Strategies returns every accepted strategy name, in declared order. Config
// validation and documentation read this rather than repeating the list.
func Strategies() []Strategy {
	return []Strategy{StrategyOrderedFailover}
}

// ValidStrategy reports whether s names an accepted strategy. The empty string
// is accepted: it means "unset", which resolves to DefaultStrategy.
//
// It fails closed on any other value. A strategy name from a later version must
// be rejected by an older binary rather than silently treated as the default,
// because silently downgrading the policy would place work somewhere the
// operator did not ask for.
func ValidStrategy(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	for _, known := range Strategies() {
		if Strategy(s) == known {
			return true
		}
	}
	return false
}

// Member is one candidate placement target, supplied in the group's declared
// order.
//
// Name is the concrete route identity that will be stamped on the bead —
// already collapsed through the caller's routing normalization, never a group
// name and never a slot-suffixed instance.
//
// Eligible is the caller's verdict for this tick. Reason explains an ineligible
// member for logs and diagnostics; it is ignored when Eligible is true.
type Member struct {
	Name     string
	Eligible bool
	Reason   string
}

// Choice is a resolved placement, carrying its own provenance the way
// config.RuntimeProviderSource does for a resolved runtime: the value alone is
// never enough to explain later why the work landed where it did.
type Choice struct {
	// Member is the chosen member's concrete route identity.
	Member string
	// Group is the group that resolved it.
	Group string
	// Strategy is the strategy that made the pick.
	Strategy Strategy
	// Reason is a short, human-readable account of the pick.
	Reason string
}

// Pick returns the placement for one group, or ok=false when the strategy is
// unknown or no member is eligible.
//
// ok=false is not an error the caller must surface: it means "make no routing
// decision this tick". A caller rebinding an existing route must then leave the
// route alone — failing closed on the last known member keeps the bead
// countable and claimable, and it recovers by itself as soon as any member
// becomes eligible again.
func Pick(group string, strategy Strategy, members []Member) (Choice, bool) {
	group = strings.TrimSpace(group)
	resolved := Strategy(strings.TrimSpace(string(strategy)))
	if resolved == "" {
		resolved = DefaultStrategy
	}
	if resolved != StrategyOrderedFailover {
		return Choice{}, false
	}
	for i := range members {
		name := strings.TrimSpace(members[i].Name)
		if name == "" || !members[i].Eligible {
			continue
		}
		return Choice{
			Member:   name,
			Group:    group,
			Strategy: resolved,
			Reason:   fmt.Sprintf("%s: first eligible member (%d of %d)", resolved, i+1, len(members)),
		}, true
	}
	return Choice{}, false
}

// IneligibleReasons returns "member: reason" for every ineligible member, in
// declared order, for a single-line log of why a group produced no choice.
func IneligibleReasons(members []Member) []string {
	var out []string
	for i := range members {
		if members[i].Eligible {
			continue
		}
		reason := strings.TrimSpace(members[i].Reason)
		if reason == "" {
			reason = "ineligible"
		}
		out = append(out, strings.TrimSpace(members[i].Name)+": "+reason)
	}
	return out
}
