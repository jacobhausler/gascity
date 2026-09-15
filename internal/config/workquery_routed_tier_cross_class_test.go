package config

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// Regression coverage for the routed Tier-3 cross-class shadow (cr-gnpx2o).
//
// The routed pool-demand tier reads two legs: the root-presence leg (graph-class
// rows -- mol-do-work step beads and workflow anchors) and the canonical leg
// (work-class rows). Each leg used to carry its own --limit and its own
// (priority, created_at, id) ordering, and the tier returned the FIRST leg
// whole whenever it was merely non-empty:
//
//	live=$(<root-present, limit 20>); [ -n "$live" ] && [ "$live" != "[]" ] \
//	  && printf "%s" "$live" || <canonical, limit 20>
//
// Priority was therefore only ever compared WITHIN a class. A pool whose routed
// queue held any ready graph row served graph rows forever and could not see a
// routed P0 work bead at all -- not "behind it in the window", absent from the
// window -- while the reconciler's count-form (which does read the canonical
// leg) kept reporting demand and spawning seats for it. Measured on the live
// city 2026-09-15: five awake mechanic-review seats worked the same handful of
// mol-do-work step rows for three hours while six P0 cr- atoms sat at the head
// of the reader's own merged order; the matching health anomaly is
// wake_no_claim (probe reports demand, hook cannot claim it, seat drains).
//
// These tests EXECUTE the generated tier against a fake ready reader, pinning
// observable service order rather than the script's spelling (the byte shape is
// pinned by TestWorkQueryGolden), and cover BOTH reader topologies because the
// shadow was topology-independent.

// routedTierGraphRows are the graph-class rows the root-presence leg serves:
// ready, routed, root-present, P2. Spellings omit the enclosing brackets so the
// fake can compose them with a row that both legs return.
const routedTierGraphRows = `{"id":"gcg-step-old","priority":2,"created_at":"2026-09-15T09:00:00Z","status":"open","metadata":{"gc.root_bead_id":"gcg-root-1","gc.kind":"workflow"}},` +
	`{"id":"gcg-step-new","priority":2,"created_at":"2026-09-15T11:00:00Z","status":"open","metadata":{"gc.root_bead_id":"gcg-root-1","gc.kind":"workflow"}}`

// routedTierWorkRows are the work-class rows the canonical leg serves: ready,
// routed, root-absent, a P0 and a P1.
const routedTierWorkRows = `{"id":"cr-p0","priority":0,"created_at":"2026-09-15T10:00:00Z","status":"open","metadata":{"gc.routed_to":"worker-pool"}},` +
	`{"id":"cr-p1","priority":1,"created_at":"2026-09-15T10:30:00Z","status":"open","metadata":{"gc.routed_to":"worker-pool"}}`

// routedTierSharedRow is returned by BOTH legs: a root-present row that is not a
// workflow root is not excluded by the canonical leg, so both readers report it.
const routedTierSharedRow = `{"id":"cr-shared","priority":1,"created_at":"2026-09-15T09:30:00Z","status":"open","metadata":{"gc.root_bead_id":"gcg-root-1","gc.routed_to":"worker-pool"}}`

// fakeRoutedTierReader answers `ready` from either reader (gc ready or bd ready)
// with the graph leg when the invocation carries the root-presence filter, and
// with the work-class leg otherwise -- the same two shapes the tier generates.
// Any other invocation answers empty, so an assertion cannot pass by accident.
func fakeRoutedTierReader(graphRows, workRows string) string {
	graph := `[` + routedTierSharedRow + `]`
	if graphRows != "" {
		graph = `[` + graphRows + `,` + routedTierSharedRow + `]`
	}
	work := `[]`
	if workRows != "" {
		work = `[` + workRows + `]`
	}
	return `#!/bin/sh
case "$*" in
  *gc.root_bead_id*) printf '%s' '` + graph + `' ;;
  *ready*) printf '%s' '` + work + `' ;;
  *) printf '[]' ;;
esac
`
}

// runRoutedReadyTier executes the generated routed tier for topo against the
// given fake reader and returns the served ids in order.
func runRoutedReadyTier(t *testing.T, topo QueryTopology, script string) []string {
	t.Helper()
	ids, stdout := runRoutedReadyTierRaw(t, topo, script)
	if ids == nil {
		t.Fatalf("routed tier did not serve a JSON array: %q", stdout)
	}
	return ids
}

// runRoutedReadyTierRaw runs the tier and reports both the ids (nil when the
// payload is not a JSON array) and the raw stdout, for the fail-open case.
func runRoutedReadyTierRaw(t *testing.T, topo QueryTopology, script string) ([]string, string) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available; the routed tier merges with jq")
	}
	out := runGeneratedQueryWithBD(t, routedReadyTierCommand(topo), map[string]string{"target": "worker-pool"}, script, script)
	if out.exit != 0 {
		t.Fatalf("routed tier exited %d: stderr=%s stdout=%q", out.exit, out.stderr, out.stdout)
	}
	var ids []string
	var rows []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.stdout)), &rows); err == nil {
		for _, row := range rows {
			id, _ := row["id"].(string)
			ids = append(ids, id)
		}
	}
	return ids, out.stdout
}

// routedTierTopologies are both reader shapes the generated query serves.
func routedTierTopologies() []struct {
	name string
	topo QueryTopology
} {
	return []struct {
		name string
		topo QueryTopology
	}{
		{"single-store", QueryTopology{}},
		{"federated", QueryTopology{FederatedReady: true}},
	}
}

// TestRoutedReadyTierServesP0WorkAheadOfGraphRows is the primary regression: a
// routed P0 work-class atom must be served ahead of ready P2 graph rows, and
// both classes must survive in the window. Before the merge the graph leg
// short-circuited the canonical leg and the result held no cr- row at all.
func TestRoutedReadyTierServesP0WorkAheadOfGraphRows(t *testing.T) {
	want := []string{"cr-p0", "cr-shared", "cr-p1", "gcg-step-old", "gcg-step-new"}
	for _, shape := range routedTierTopologies() {
		t.Run(shape.name, func(t *testing.T) {
			ids := runRoutedReadyTier(t, shape.topo, fakeRoutedTierReader(routedTierGraphRows, routedTierWorkRows))
			if len(ids) == 0 || ids[0] != "cr-p0" {
				t.Fatalf("routed P0 work row was not served first (cross-class shadow): got %v want %v", ids, want)
			}
			if strings.Join(ids, ",") != strings.Join(want, ",") {
				t.Fatalf("merged window order wrong: got %v want %v", ids, want)
			}
		})
	}
}

// TestRoutedReadyTierStillServesGraphDemandWhenNoWorkExists guards the
// over-correction: a "fix" that silenced the graph leg to stop the churn would
// satisfy the ordering test above while starving real graph-class demand.
func TestRoutedReadyTierStillServesGraphDemandWhenNoWorkExists(t *testing.T) {
	want := []string{"cr-shared", "gcg-step-old", "gcg-step-new"}
	for _, shape := range routedTierTopologies() {
		t.Run(shape.name, func(t *testing.T) {
			ids := runRoutedReadyTier(t, shape.topo, fakeRoutedTierReader(routedTierGraphRows, ""))
			if strings.Join(ids, ",") != strings.Join(want, ",") {
				t.Fatalf("graph-class demand stopped being served: got %v want %v", ids, want)
			}
		})
	}
}

// TestRoutedReadyTierDoesNotServeADuplicateRow pins the dedupe: a row that
// satisfies both legs gets one slot of the bounded window, not two.
func TestRoutedReadyTierDoesNotServeADuplicateRow(t *testing.T) {
	for _, shape := range routedTierTopologies() {
		t.Run(shape.name, func(t *testing.T) {
			ids := runRoutedReadyTier(t, shape.topo, fakeRoutedTierReader(routedTierGraphRows, routedTierWorkRows))
			seen := map[string]int{}
			for _, id := range ids {
				seen[id]++
			}
			if seen["cr-shared"] != 1 {
				t.Fatalf("row present in both legs was served %d times: %v", seen["cr-shared"], ids)
			}
		})
	}
}

// TestRoutedReadyTierEmptyWhenNeitherLegHasDemand keeps the no-demand answer at
// an empty array: the caller treats "[]" as "no demand" and falls through to the
// migration and ephemeral tiers, and a merge that emitted null or an empty
// string would change that fall-through.
func TestRoutedReadyTierEmptyWhenNeitherLegHasDemand(t *testing.T) {
	for _, shape := range routedTierTopologies() {
		t.Run(shape.name, func(t *testing.T) {
			empty := fakeRoutedTierReader("", "")
			_, stdout := runRoutedReadyTierRaw(t, shape.topo, empty)
			if got := strings.TrimSpace(stdout); got != "[]" {
				t.Fatalf("no-demand answer must be an empty array, got %q", stdout)
			}
		})
	}
}

// TestRoutedReadyTierPreservesMalformedPayloadForFailOpen keeps the hook's
// fail-open contract: a malformed reader payload is passed through for the
// hook's existing handling instead of being converted into false-empty demand by
// the merge (the same promise preferExecutablePoolDemandScript makes).
func TestRoutedReadyTierPreservesMalformedPayloadForFailOpen(t *testing.T) {
	for _, shape := range routedTierTopologies() {
		t.Run(shape.name, func(t *testing.T) {
			malformed := fakeRoutedTierReader("not json at all", "")
			_, stdout := runRoutedReadyTierRaw(t, shape.topo, malformed)
			if !strings.Contains(stdout, "not json at all") {
				t.Fatalf("malformed payload was converted into false-empty demand: stdout=%q", stdout)
			}
		})
	}
}
