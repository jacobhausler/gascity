package agentgroup

import (
	"strings"
	"testing"
)

func eligible(name string) Member   { return Member{Name: name, Eligible: true} }
func ineligible(n, r string) Member { return Member{Name: n, Reason: r} }

func TestPickOrderedFailover(t *testing.T) {
	tests := []struct {
		name     string
		strategy Strategy
		members  []Member
		wantOK   bool
		want     string
	}{
		{
			name:     "first member wins when eligible",
			strategy: StrategyOrderedFailover,
			members:  []Member{eligible("rig/a"), eligible("rig/b")},
			wantOK:   true,
			want:     "rig/a",
		},
		{
			name:     "falls through to the second when the first is ineligible",
			strategy: StrategyOrderedFailover,
			members:  []Member{ineligible("rig/a", "agent suspended"), eligible("rig/b")},
			wantOK:   true,
			want:     "rig/b",
		},
		{
			name:     "falls through past several ineligible members",
			strategy: StrategyOrderedFailover,
			members:  []Member{ineligible("rig/a", "x"), ineligible("rig/b", "y"), eligible("rig/c")},
			wantOK:   true,
			want:     "rig/c",
		},
		{
			name:     "no eligible member makes no choice",
			strategy: StrategyOrderedFailover,
			members:  []Member{ineligible("rig/a", "x"), ineligible("rig/b", "y")},
			wantOK:   false,
		},
		{
			name:     "returns to the preferred member once it recovers",
			strategy: StrategyOrderedFailover,
			members:  []Member{eligible("rig/a"), eligible("rig/b")},
			wantOK:   true,
			want:     "rig/a",
		},
		{
			name:     "unset strategy resolves to the default",
			strategy: "",
			members:  []Member{eligible("rig/a")},
			wantOK:   true,
			want:     "rig/a",
		},
		{
			name:     "empty member list makes no choice",
			strategy: StrategyOrderedFailover,
			wantOK:   false,
		},
		{
			name:     "a blank member name is never chosen",
			strategy: StrategyOrderedFailover,
			members:  []Member{{Name: "   ", Eligible: true}, eligible("rig/b")},
			wantOK:   true,
			want:     "rig/b",
		},
		{
			name:     "an unknown strategy fails closed",
			strategy: Strategy("least-loaded"),
			members:  []Member{eligible("rig/a")},
			wantOK:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Pick("rig/heavy", tc.strategy, tc.members)
			if ok != tc.wantOK {
				t.Fatalf("Pick ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				if got != (Choice{}) {
					t.Fatalf("Pick returned %+v alongside ok=false", got)
				}
				return
			}
			if got.Member != tc.want {
				t.Errorf("Member = %q, want %q", got.Member, tc.want)
			}
			if got.Group != "rig/heavy" {
				t.Errorf("Group = %q, want %q", got.Group, "rig/heavy")
			}
			if got.Strategy != StrategyOrderedFailover {
				t.Errorf("Strategy = %q, want %q", got.Strategy, StrategyOrderedFailover)
			}
			if !strings.Contains(got.Reason, string(StrategyOrderedFailover)) {
				t.Errorf("Reason = %q, want it to name the strategy", got.Reason)
			}
		})
	}
}

// A merely busy member is still a candidate: v1 has no capacity gate, because
// routing to a saturated pool is harmless — the bead waits in that pool's queue
// and the pull system serves it when a seat frees.
func TestPickChoosesABusyButEligibleMember(t *testing.T) {
	got, ok := Pick("rig/heavy", StrategyOrderedFailover, []Member{eligible("rig/full"), eligible("rig/idle")})
	if !ok || got.Member != "rig/full" {
		t.Fatalf("Pick = %+v, %v; want the first eligible member regardless of load", got, ok)
	}
}

func TestPickIsDeterministic(t *testing.T) {
	members := []Member{ineligible("rig/a", "x"), eligible("rig/b"), eligible("rig/c")}
	first, ok := Pick("rig/heavy", StrategyOrderedFailover, members)
	if !ok {
		t.Fatal("Pick returned no choice")
	}
	for i := 0; i < 50; i++ {
		got, ok := Pick("rig/heavy", StrategyOrderedFailover, members)
		if !ok || got != first {
			t.Fatalf("Pick = %+v, %v on iteration %d; want the stable %+v", got, ok, i, first)
		}
	}
}

func TestValidStrategy(t *testing.T) {
	for _, s := range []string{"", "  ", "ordered-failover"} {
		if !ValidStrategy(s) {
			t.Errorf("ValidStrategy(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"least-loaded", "round-robin", "Ordered-Failover", "random"} {
		if ValidStrategy(s) {
			t.Errorf("ValidStrategy(%q) = true, want false (must fail closed)", s)
		}
	}
}

func TestStrategiesIsTheSingleSourceOfAcceptedNames(t *testing.T) {
	got := Strategies()
	if len(got) != 1 || got[0] != StrategyOrderedFailover {
		t.Fatalf("Strategies() = %v, want exactly [%s]", got, StrategyOrderedFailover)
	}
}

func TestIneligibleReasons(t *testing.T) {
	got := IneligibleReasons([]Member{
		eligible("rig/a"),
		ineligible("rig/b", "agent suspended"),
		{Name: "rig/c"},
	})
	want := []string{"rig/b: agent suspended", "rig/c: ineligible"}
	if len(got) != len(want) {
		t.Fatalf("IneligibleReasons = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("IneligibleReasons[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
