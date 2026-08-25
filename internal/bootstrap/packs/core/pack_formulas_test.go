package core

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// formulaFile is the subset of a formula TOML these tests inspect. Steps carry
// the agent-facing instructions, so asserting on a step description is how the
// pack pins behavior that lives in prompt text rather than in Go.
type formulaFile struct {
	Formula string `toml:"formula"`
	Steps   []struct {
		ID          string `toml:"id"`
		Title       string `toml:"title"`
		Description string `toml:"description"`
	} `toml:"steps"`
}

// readFormula decodes a formula TOML from the embedded core pack.
//
// file stays parameterized because callers pin behavior across the embedded
// core pack's formulas, including mol-polecat-base.toml and mol-do-work.toml.
// The pack also ships sibling formulas (mol-polecat-commit, mol-polecat-report)
// that inherit these steps, so tests for any of them read through this helper.
//
//nolint:unparam // see above
func readFormula(t *testing.T, file string) formulaFile {
	t.Helper()
	data, err := fs.ReadFile(PackFS, "formulas/"+file)
	if err != nil {
		t.Fatalf("reading formulas/%s: %v", file, err)
	}
	var parsed formulaFile
	if _, err := toml.Decode(string(data), &parsed); err != nil {
		t.Fatalf("decoding formulas/%s: %v", file, err)
	}
	return parsed
}

// formulaStep returns the description of the named step, failing the test when
// the step is absent.
func formulaStep(t *testing.T, f formulaFile, id string) string {
	t.Helper()
	for _, step := range f.Steps {
		if step.ID == id {
			return step.Description
		}
	}
	t.Fatalf("formula %s has no step %q", f.Formula, id)
	return ""
}

// TestMolDoWorkClaimsSourceBeforeFirstHeartbeat pins the ownership boundary
// between the routed formula step and the convoy child it actually works.
// Routing claims the step (which may be a gcg wrapper), while this formula
// reads and heartbeats WORK_BEAD_ID. The source claim must therefore happen
// first, and a failed claim must fail closed before any work starts.
func TestMolDoWorkClaimsSourceBeforeFirstHeartbeat(t *testing.T) {
	step := formulaStep(t, readFormula(t, "mol-do-work.toml"), "do-work")

	claim := `gc bd update "$WORK_BEAD_ID" --claim`
	firstHeartbeat := `gc bd heartbeat "$WORK_BEAD_ID"`
	claimAt := strings.Index(step, claim)
	if claimAt < 0 {
		t.Fatalf("do-work must claim the source WORK_BEAD_ID before heartbeating it: missing %q", claim)
	}
	heartbeatAt := strings.Index(step, firstHeartbeat)
	if heartbeatAt < 0 {
		t.Fatalf("do-work must heartbeat the source WORK_BEAD_ID: missing %q", firstHeartbeat)
	}
	if claimAt > heartbeatAt {
		t.Fatalf("source claim at byte %d follows first source heartbeat at byte %d", claimAt, heartbeatAt)
	}

	// A by-ID claim can lose ownership or fail to route. The formula must not
	// proceed under an assumption that the source is owned, so the claim is
	// guarded and exits before the first heartbeat on failure.
	if !strings.Contains(step, "if ! "+claim+"; then") {
		t.Fatalf("source claim must be guarded with `if ! ...; then` so claim failure stops execution")
	}
	claimGuardAt := strings.Index(step, "if ! "+claim+"; then")
	firstHeartbeatAt := strings.Index(step, firstHeartbeat)
	claimGuard := step[claimGuardAt:firstHeartbeatAt]
	if !strings.Contains(claimGuard, "exit 1") {
		t.Fatalf("source claim failure must exit before the first heartbeat; guard is %q", claimGuard)
	}

	// The first heartbeat is itself an ownership check. A failed refresh means
	// the source lease is gone, so implementation instructions must not follow
	// a bare heartbeat that leaves the worker running without a live claim.
	implementationAt := strings.Index(step, "**2. Implement the solution and verify it:")
	if implementationAt < 0 {
		t.Fatal("do-work is missing its implementation section")
	}
	heartbeatGuard := step[firstHeartbeatAt:implementationAt]
	if !strings.Contains(heartbeatGuard, firstHeartbeat+" ||") {
		t.Fatalf("first source heartbeat must fail closed with `||` before implementation begins; guard is %q", heartbeatGuard)
	}
	if !strings.Contains(heartbeatGuard, "exit 1") {
		t.Fatalf("first source heartbeat failure must exit before implementation begins; guard is %q", heartbeatGuard)
	}

	// The routed gcg wrapper is a control-plane step, not the source work bead;
	// no liveness command may accidentally target it.
	for _, line := range strings.Split(step, "\n") {
		if !strings.Contains(line, "gc bd heartbeat") {
			continue
		}
		if !strings.Contains(line, `"$WORK_BEAD_ID"`) {
			t.Errorf("heartbeat must target source WORK_BEAD_ID, got %q", strings.TrimSpace(line))
		}
		if strings.Contains(line, "GC_BEAD_ID") {
			t.Errorf("heartbeat must never target the gcg wrapper GC_BEAD_ID, got %q", strings.TrimSpace(line))
		}
	}

	// Do not teach workers that an unconditional by-ID claim is safe despite
	// ownership. Conflicts and routing failures must remain visible and stop.
	lower := strings.ToLower(step)
	for _, unsafe := range []string{
		"safe even if",
		"regardless of current state",
		"whether or not something else already holds",
	} {
		if strings.Contains(lower, unsafe) {
			t.Errorf("do-work must not describe source claiming as unconditionally safe; found %q", unsafe)
		}
	}
}

// TestMolDoWorkDrainResolvesCurrentContinuation pins the drain handoff
// contract. Pool shells do not receive the dispatch-only bead-id variables, so
// the drain seeds resolution from gc hook current and permits those vars only
// after a successful current-claim read. A stale current bead is not closed:
// its root selects the unique ready mol-do-work.drain continuation. Every
// ambiguity, identity mismatch, read failure, or close failure stops before
// drain-ack.
func TestMolDoWorkDrainResolvesCurrentContinuation(t *testing.T) {
	step := formulaStep(t, readFormula(t, "mol-do-work.toml"), "drain")

	actor := `RUNTIME_ACTOR="${BEADS_ACTOR:-}"`
	actorAt := strings.Index(step, actor)
	if actorAt < 0 {
		t.Fatalf("drain must derive its runtime actor from BEADS_ACTOR: missing %q", actor)
	}
	if !strings.Contains(step[actorAt:], `if [ -z "$RUNTIME_ACTOR" ]; then`) {
		t.Fatal("drain must fail closed when BEADS_ACTOR is missing")
	}
	currentID := `CURRENT_BEAD_ID="$(gc hook current --id-only 2>/dev/null)"`
	currentIDAt := strings.Index(step, currentID)
	if currentIDAt < 0 {
		t.Fatalf("drain must seed resolution from gc hook current: missing %q", currentID)
	}
	seedID := `STARTUP_BEAD_ID="${CURRENT_BEAD_ID:-${GC_BEAD_ID:-${GC_TRIGGER_BEAD_ID:-}}}"`
	seedIDAt := strings.Index(step, seedID)
	if seedIDAt < 0 || seedIDAt < currentIDAt {
		t.Fatalf("drain must use current claim before dispatch-env fallback: missing or misplaced %q", seedID)
	}

	if strings.Contains(step, `DRAIN_BEAD_ID="${GC_BEAD_ID`) {
		t.Fatal("drain must not close the immutable startup env bead directly")
	}
	if !strings.Contains(step, `gc bd show "$STARTUP_BEAD_ID" --json`) {
		t.Fatal("drain must read the seed bead before selecting a continuation")
	}
	if !strings.Contains(step, `STARTUP_PAYLOAD_ID=$(printf '%s' "$STARTUP_BEAD" | jq -r`) ||
		!strings.Contains(step, `if [ "$STARTUP_PAYLOAD_ID" != "$STARTUP_BEAD_ID" ]; then`) {
		t.Fatal("drain must revalidate that the seed payload is the requested bead")
	}
	if !strings.Contains(step, `.metadata["gc.root_bead_id"] // empty`) ||
		!strings.Contains(step, `.metadata["gc.step_ref"] // empty`) {
		t.Fatal("drain must derive both workflow root and step ref from the seed bead")
	}

	// Fresh current-drain claims may close directly, but only while the seed is
	// visibly live and exactly the drain step for the resolved root.
	direct := `if [ "$STARTUP_STEP_REF" = "mol-do-work.drain" ] && [ "$STARTUP_STATUS" = "in_progress" ] && [ "$STARTUP_ASSIGNEE" = "$RUNTIME_ACTOR" ] && [ -n "$ROOT_BEAD_ID" ]; then`
	if !strings.Contains(step, direct) {
		t.Fatalf("fresh current-drain path must require exact step, live status, assignee, and root: missing %q", direct)
	}
	if !strings.Contains(step, `[ -z "$STARTUP_ASSIGNEE" ] || [ "$STARTUP_ASSIGNEE" != "$RUNTIME_ACTOR" ]`) {
		t.Fatal("drain must reject a seed bead with missing or foreign ownership")
	}

	// A stale do-work seed must resolve through the federated reader so split
	// graph stores are covered; the old `gc bd ready` path is store-blind.
	ready := `gc ready --json --limit=2 --metadata-field "gc.root_bead_id=$ROOT_BEAD_ID" --metadata-field "gc.step_ref=mol-do-work.drain"`
	if !strings.Contains(step, ready) {
		t.Fatalf("stale seeds must use the federated continuation reader: missing %q", ready)
	}
	if strings.Contains(step, "gc bd ready") {
		t.Fatal("drain must never use split-store-blind `gc bd ready`; use federated `gc ready`")
	}
	if !strings.Contains(step, "length == 1") {
		t.Fatal("drain must require exactly one ready continuation, rejecting zero and ambiguous candidates")
	}
	for _, field := range []string{"CANDIDATE_ID", "CANDIDATE_ROOT", "CANDIDATE_STEP", "CANDIDATE_STATUS", "CANDIDATE_ASSIGNEE"} {
		if !strings.Contains(step, field) {
			t.Fatalf("drain must revalidate candidate %s before closing", field)
		}
	}
	if !strings.Contains(step, `CANDIDATE_ROOT" != "$ROOT_BEAD_ID"`) ||
		!strings.Contains(step, `CANDIDATE_STEP" != "mol-do-work.drain"`) ||
		!strings.Contains(step, `CANDIDATE_STATUS" != "open"`) ||
		!strings.Contains(step, `[ -n "$CANDIDATE_ASSIGNEE" ] && [ "$CANDIDATE_ASSIGNEE" != "$RUNTIME_ACTOR" ]`) {
		t.Fatal("drain must reject a candidate whose root, step, status, or foreign assignee is not the expected continuation")
	}

	emptyGuard := `if [ -z "$STARTUP_BEAD_ID" ]; then`
	emptyGuardAt := strings.Index(step, emptyGuard)
	if emptyGuardAt < 0 {
		t.Fatalf("drain must fail closed when no current or fallback seed id is available: missing %q", emptyGuard)
	}

	claimCommand := `gc bd update "$DRAIN_BEAD_ID" --claim`
	claimAt := strings.Index(step, claimCommand)
	if claimAt < 0 {
		t.Fatalf("drain must claim the resolved step before closing it: missing %q", claimCommand)
	}
	closeCommand := `gc bd update "$DRAIN_BEAD_ID" --set-metadata gc.outcome=pass --status=closed`
	closeAt := strings.Index(step, closeCommand)
	if closeAt < 0 {
		t.Fatalf("drain must close the exact resolved step id: missing %q", closeCommand)
	}
	for _, unsupported := range []string{"--if-status", "--if-assignee"} {
		if strings.Contains(step, unsupported) {
			t.Fatalf("drain close must not use unsupported routed update flag %q", unsupported)
		}
	}
	ack := "gc runtime drain-ack"
	ackAt := strings.Index(step, ack)
	if ackAt < 0 {
		t.Fatalf("drain must acknowledge the runtime after closing its step: missing %q", ack)
	}
	if closeAt > ackAt {
		t.Fatalf("drain acknowledges runtime before closing its step (close at %d, ack at %d)", closeAt, ackAt)
	}
	if claimAt > closeAt {
		t.Fatalf("drain closes its step before claiming it (claim at %d, close at %d)", claimAt, closeAt)
	}
	claimGuard := "if ! " + claimCommand + "; then"
	claimGuardAt := strings.Index(step, claimGuard)
	if claimGuardAt < 0 {
		t.Fatalf("drain must fail closed when claiming the resolved step fails: missing %q", claimGuard)
	}
	if claimGuardAt > closeAt {
		t.Fatalf("drain closes its step before entering the claim failure guard (guard at %d, close at %d)", claimGuardAt, closeAt)
	}
	claimGuardBody := step[claimGuardAt:closeAt]
	if !strings.Contains(claimGuardBody, "exit 1") {
		t.Fatalf("drain claim failure must exit before close; guard is %q", claimGuardBody)
	}

	// The empty-id guard must terminate before the close attempt, so a failed
	// current-claim lookup cannot be converted into a successful drain-ack.
	emptyGuardBody := step[emptyGuardAt:closeAt]
	if !strings.Contains(emptyGuardBody, "exit 1") {
		t.Fatalf("drain must exit from the no-id guard before attempting close; guard is %q", emptyGuardBody)
	}

	closeGuard := "if ! " + closeCommand + "; then"
	closeGuardAt := strings.Index(step, closeGuard)
	if closeGuardAt < 0 {
		t.Fatalf("drain must fail closed when closing the exact step fails: missing %q", closeGuard)
	}
	if closeGuardAt > ackAt {
		t.Fatalf("drain acknowledges runtime before entering the close failure guard (guard at %d, ack at %d)", closeGuardAt, ackAt)
	}
	closeGuardBody := step[closeGuardAt:ackAt]
	if !strings.Contains(closeGuardBody, "exit 1") {
		t.Fatalf("drain close failure must exit before drain-ack; guard is %q", closeGuardBody)
	}
}

// TestMolDoWorkDrainFailsClosedWhenCurrentClaimCannotBeRead pins the
// session-front-door boundary: a hook-current identity/read error must not be
// converted into an environment fallback that can name a different bead.
func TestMolDoWorkDrainFailsClosedWhenCurrentClaimCannotBeRead(t *testing.T) {
	step := formulaStep(t, readFormula(t, "mol-do-work.toml"), "drain")

	if strings.Contains(step, `gc hook current --id-only 2>/dev/null || true`) {
		t.Fatal("drain must not ignore gc hook current errors before selecting a bead")
	}
	guard := `if ! CURRENT_BEAD_ID="$(gc hook current --id-only 2>/dev/null)"; then`
	if !strings.Contains(step, guard) {
		t.Fatalf("drain must fail closed around the current-claim read: missing %q", guard)
	}
	guardAt := strings.Index(step, guard)
	guardEnd := strings.Index(step[guardAt:], "\nfi")
	if guardEnd < 0 || !strings.Contains(step[guardAt:guardAt+guardEnd], "exit 1") {
		t.Fatal("current-claim read failure must exit before any fallback seed or bead read")
	}
}

func TestMolDoWorkUsesAppendNotes(t *testing.T) {
	formula := readFormula(t, "mol-do-work.toml")
	for _, step := range formula.Steps {
		if strings.Contains(strings.ReplaceAll(step.Description, "--append-notes", ""), "--notes") {
			t.Fatalf("mol-do-work step %q must not use destructive --notes; use --append-notes", step.ID)
		}
	}
}

// TestMolDoWorkDrainCloseUsesNativeGraphUpdateFlags pins the graph-store
// boundary for the class-owned drain step. The closed graph update parser only
// serves fields represented by its native update object; drain must not emit
// bd-only note flags that the graph path rejects.
func TestMolDoWorkDrainCloseUsesNativeGraphUpdateFlags(t *testing.T) {
	step := formulaStep(t, readFormula(t, "mol-do-work.toml"), "drain")

	var closeLine string
	for _, line := range strings.Split(step, "\n") {
		if strings.Contains(line, `gc bd update "$DRAIN_BEAD_ID"`) && strings.Contains(line, "--status=closed") {
			closeLine = line
			break
		}
	}
	if closeLine == "" {
		t.Fatal("drain must close DRAIN_BEAD_ID through a native graph update")
	}
	if strings.Contains(closeLine, "--append-notes") {
		t.Fatalf("drain close emits unsupported graph update flag --append-notes: %q", closeLine)
	}
}

const fakeDrainGC = `#!/bin/sh
set -eu
if [ "${1:-}" = "hook" ] && [ "${2:-}" = "current" ]; then
  if [ "${FAKE_CURRENT_RC:-0}" != "0" ]; then exit 1; fi
  printf '%s\n' "${FAKE_CURRENT_ID:-}"
  exit 0
fi
if [ "${1:-}" = "bd" ] && [ "${2:-}" = "show" ]; then
	printf 'show:%s\n' "${3:-}" >> "$FAKE_LOG"
  if [ "${FAKE_SHOW_RC:-0}" != "0" ]; then exit 1; fi
  printf '%s\n' "${FAKE_SEED_JSON:-[]}"
  exit 0
fi
if [ "${1:-}" = "ready" ]; then
  printf '%s\n' "ready" >> "$FAKE_LOG"
  if [ "${FAKE_READY_RC:-0}" != "0" ]; then exit 1; fi
  printf '%s\n' "${FAKE_READY_JSON:-[]}"
  exit 0
fi
if [ "${1:-}" = "bd" ] && [ "${2:-}" = "update" ]; then
	if [ "${4:-}" = "--claim" ]; then
		printf 'claim-attempt:%s\n' "${3:-}" >> "$FAKE_LOG"
		if [ "${FAKE_CLAIM_RC:-0}" != "0" ]; then exit 1; fi
		printf 'claim:%s\n' "${3:-}" >> "$FAKE_LOG"
		exit 0
	fi
	printf 'attempt:%s\n' "${3:-}" >> "$FAKE_LOG"
	if [ "${FAKE_UPDATE_RC:-0}" != "0" ]; then exit 1; fi
	printf 'update:%s\n' "${3:-}" >> "$FAKE_LOG"
	exit 0
fi
if [ "${1:-}" = "runtime" ] && [ "${2:-}" = "drain-ack" ]; then
  printf '%s\n' "ack" >> "$FAKE_LOG"
  exit 0
fi
exit 64
`

type drainHarnessCase struct {
	name          string
	actor         string
	missingActor  bool
	currentID     string
	currentFailed bool
	showFailed    bool
	claimFailed   bool
	readyFailed   bool
	updateFailed  bool
	seedJSON      string
	readyJSON     string
	env           map[string]string
	wantSuccess   bool
	wantID        string
	wantReady     bool
}

func drainHarnessBead(id, step, assignee string) string {
	return "[" + drainHarnessBeadObject(id, "root-1", step, "in_progress", assignee) + "]"
}

func drainHarnessBeadObject(id, root, step, status, assignee string) string {
	return fmt.Sprintf(`{
  "id": %q,
  "status": %q,
  "assignee": %q,
  "metadata": {"gc.root_bead_id": %q, "gc.step_ref": %q}
}`, id, status, assignee, root, step)
}

func drainHarnessEnv(overrides map[string]string, jqPath string) []string {
	skip := map[string]bool{
		"BEADS_ACTOR":        true,
		"GC_BEAD_ID":         true,
		"GC_TRIGGER_BEAD_ID": true,
		"FAKE_CURRENT_ID":    true,
		"FAKE_CURRENT_RC":    true,
		"FAKE_CLAIM_RC":      true,
		"FAKE_LOG":           true,
		"FAKE_READY_JSON":    true,
		"FAKE_READY_RC":      true,
		"FAKE_SEED_JSON":     true,
		"FAKE_SHOW_RC":       true,
		"FAKE_UPDATE_RC":     true,
		"PATH":               true,
	}
	env := make([]string, 0, len(os.Environ())+len(overrides)+1)
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if !ok || skip[key] {
			continue
		}
		env = append(env, item)
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	path := overrides["PATH"]
	if path == "" {
		path = filepath.Dir(jqPath)
	}
	env = append(env, "PATH="+path)
	return env
}

func runDrainHarness(t *testing.T, script string, tc drainHarnessCase) (string, string, error) {
	t.Helper()
	jqPath, err := exec.LookPath("jq")
	if err != nil {
		t.Fatalf("jq is required to execute the formula drain contract: %v", err)
	}
	dir := t.TempDir()
	gcPath := filepath.Join(dir, "gc")
	if err := os.WriteFile(gcPath, []byte(fakeDrainGC), 0o755); err != nil {
		t.Fatalf("write fake gc: %v", err)
	}
	logPath := filepath.Join(dir, "calls.log")
	env := map[string]string{
		"BEADS_ACTOR":     tc.actor,
		"FAKE_CURRENT_ID": tc.currentID,
		"FAKE_CURRENT_RC": "0",
		"FAKE_CLAIM_RC":   "0",
		"FAKE_LOG":        logPath,
		"FAKE_READY_JSON": tc.readyJSON,
		"FAKE_READY_RC":   "0",
		"FAKE_SEED_JSON":  tc.seedJSON,
		"FAKE_SHOW_RC":    "0",
		"FAKE_UPDATE_RC":  "0",
	}
	if tc.currentFailed {
		env["FAKE_CURRENT_RC"] = "1"
	}
	if tc.claimFailed {
		env["FAKE_CLAIM_RC"] = "1"
	}
	if tc.actor == "" && !tc.missingActor {
		env["BEADS_ACTOR"] = "worker"
	}
	if tc.missingActor {
		delete(env, "BEADS_ACTOR")
	}
	if tc.showFailed {
		env["FAKE_SHOW_RC"] = "1"
	}
	if tc.readyFailed {
		env["FAKE_READY_RC"] = "1"
	}
	if tc.updateFailed {
		env["FAKE_UPDATE_RC"] = "1"
	}
	for key, value := range tc.env {
		env[key] = value
	}
	// Put the fake gc first and retain the real jq directory. The formula's
	// shell is otherwise run with only scenario-controlled bead-store behavior.
	path := dir + string(os.PathListSeparator) + filepath.Dir(jqPath)
	env["PATH"] = path
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = drainHarnessEnv(env, jqPath)
	out, runErr := cmd.CombinedOutput()
	logBytes, readErr := os.ReadFile(logPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read fake gc log: %v", readErr)
	}
	return string(out), string(logBytes), runErr
}

// TestMolDoWorkDrainShellScenarios executes the embedded drain instructions
// against a hermetic fake gc. This covers the live current-claim path, stale
// same-session continuation, explicit startup-seed fallback, zero/ambiguous/
// wrong candidates, reader failures, and close failures; every failure must
// omit drain-ack.
func TestMolDoWorkDrainShellScenarios(t *testing.T) {
	step := formulaStep(t, readFormula(t, "mol-do-work.toml"), "drain")
	start := strings.Index(step, "```bash\n")
	if start < 0 {
		t.Fatal("drain is missing its bash command block")
	}
	bodyStart := start + len("```bash\n")
	bodyEndRel := strings.Index(step[bodyStart:], "\n```")
	if bodyEndRel < 0 {
		t.Fatal("drain bash command block is unterminated")
	}
	script := step[bodyStart : bodyStart+bodyEndRel]

	seedDrain := drainHarnessBead("drain-current", "mol-do-work.drain", "worker")
	seedDrainForeign := drainHarnessBead("drain-current", "mol-do-work.drain", "other-worker")
	seedWork := drainHarnessBead("work-old", "mol-do-work.do-work", "worker")
	seedWorkForeign := drainHarnessBead("work-old", "mol-do-work.do-work", "other-worker")
	readyDrain := drainHarnessBeadObject("drain-next", "root-1", "mol-do-work.drain", "open", "")
	readyDrainOwned := drainHarnessBeadObject("drain-next", "root-1", "mol-do-work.drain", "open", "worker")
	wrongRoot := drainHarnessBeadObject("drain-wrong-root", "root-2", "mol-do-work.drain", "open", "")
	wrongStep := drainHarnessBeadObject("drain-wrong-step", "root-1", "mol-do-work.do-work", "open", "")
	wrongStatus := drainHarnessBeadObject("drain-wrong-status", "root-1", "mol-do-work.drain", "in_progress", "")
	foreignAssignee := drainHarnessBeadObject("drain-foreign", "root-1", "mol-do-work.drain", "open", "other-worker")
	for _, tc := range []drainHarnessCase{
		{
			name:        "fresh current drain",
			currentID:   "drain-current",
			seedJSON:    seedDrain,
			readyJSON:   `[]`,
			wantSuccess: true,
			wantID:      "drain-current",
		},
		{
			name:         "missing actor",
			missingActor: true,
			currentID:    "drain-current",
			seedJSON:     seedDrain,
			readyJSON:    `[]`,
		},
		{
			name:        "claim failure",
			currentID:   "drain-current",
			claimFailed: true,
			seedJSON:    seedDrain,
			readyJSON:   `[]`,
		},
		{
			name:      "current direct foreign seed",
			currentID: "drain-current",
			seedJSON:  seedDrainForeign,
			readyJSON: `[]`,
		},
		{
			name:          "current hook failure rejects env fallback",
			currentFailed: true,
			seedJSON:      seedDrainForeign,
			readyJSON:     `[]`,
			env:           map[string]string{"GC_BEAD_ID": "drain-current"},
		},
		{
			name:      "stale foreign seed",
			currentID: "work-old",
			seedJSON:  seedWorkForeign,
			readyJSON: `[]`,
		},
		{
			name:        "stale current selects unassigned continuation before stale env",
			currentID:   "work-old",
			seedJSON:    seedWork,
			readyJSON:   "[" + readyDrain + "]",
			env:         map[string]string{"GC_BEAD_ID": "stale-env-drain"},
			wantSuccess: true,
			wantID:      "drain-next",
			wantReady:   true,
		},
		{
			name:        "stale current selects seed-owned continuation",
			currentID:   "work-old",
			seedJSON:    seedWork,
			readyJSON:   "[" + readyDrainOwned + "]",
			wantSuccess: true,
			wantID:      "drain-next",
			wantReady:   true,
		},
		{
			name:          "current hook failure rejects direct env seed",
			currentFailed: true,
			seedJSON:      seedDrain,
			readyJSON:     `[]`,
			env:           map[string]string{"GC_TRIGGER_BEAD_ID": "drain-current"},
		},
		{
			name:          "no seed id",
			currentFailed: true,
			readyJSON:     `[]`,
		},
		{
			name:      "zero continuations",
			currentID: "work-old",
			seedJSON:  seedWork,
			readyJSON: `[]`,
			wantReady: true,
		},
		{
			name:      "ambiguous continuations",
			currentID: "work-old",
			seedJSON:  seedWork,
			readyJSON: "[" + readyDrain + "," + readyDrainOwned + "]",
			wantReady: true,
		},
		{
			name:      "wrong root",
			currentID: "work-old",
			seedJSON:  seedWork,
			readyJSON: "[" + wrongRoot + "]",
			wantReady: true,
		},
		{
			name:      "wrong step",
			currentID: "work-old",
			seedJSON:  seedWork,
			readyJSON: "[" + wrongStep + "]",
			wantReady: true,
		},
		{
			name:      "wrong status",
			currentID: "work-old",
			seedJSON:  seedWork,
			readyJSON: "[" + wrongStatus + "]",
			wantReady: true,
		},
		{
			name:      "foreign assignee",
			currentID: "work-old",
			seedJSON:  seedWork,
			readyJSON: "[" + foreignAssignee + "]",
			wantReady: true,
		},
		{
			name:       "seed read failure",
			currentID:  "drain-current",
			showFailed: true,
			seedJSON:   seedDrain,
			readyJSON:  `[]`,
		},
		{
			name:      "wrong seed id",
			currentID: "drain-current",
			seedJSON:  drainHarnessBead("different-seed", "mol-do-work.drain", "worker"),
			readyJSON: `[]`,
		},
		{
			name:        "ready read failure",
			currentID:   "work-old",
			seedJSON:    seedWork,
			readyFailed: true,
			readyJSON:   `[]`,
			wantReady:   true,
		},
		{
			name:         "close failure",
			currentID:    "drain-current",
			seedJSON:     seedDrain,
			updateFailed: true,
			readyJSON:    `[]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, log, err := runDrainHarness(t, script, tc)
			if tc.wantSuccess && err != nil {
				t.Fatalf("drain failed: %v\noutput: %s\nlog: %s", err, out, log)
			}
			if !tc.wantSuccess && err == nil {
				t.Fatalf("drain succeeded on a fail-closed scenario\noutput: %s\nlog: %s", out, log)
			}
			if tc.wantID != "" && !strings.Contains(log, "update:"+tc.wantID+"\n") {
				t.Fatalf("close log = %q, want update of exact bead %q", log, tc.wantID)
			}
			if tc.wantReady && !strings.Contains(log, "ready\n") {
				t.Fatalf("close log = %q, want federated ready lookup", log)
			}
			if tc.currentFailed && strings.Contains(log, "show:") {
				t.Fatalf("close log = %q, current-claim failure must stop before reading a fallback seed bead", log)
			}
			if !tc.wantReady && strings.Contains(log, "ready\n") {
				t.Fatalf("close log = %q, did not expect ready lookup", log)
			}
			if tc.wantSuccess {
				if !strings.Contains(log, "claim:"+tc.wantID+"\n") {
					t.Fatalf("close log = %q, want claim of exact bead %q", log, tc.wantID)
				}
				if !strings.Contains(log, "ack\n") {
					t.Fatalf("close log = %q, want drain-ack after close", log)
				}
				if strings.Index(log, "claim:") > strings.Index(log, "update:") {
					t.Fatalf("close log = %q, close preceded claim", log)
				}
				if strings.Index(log, "update:") > strings.Index(log, "ack\n") {
					t.Fatalf("close log = %q, drain-ack preceded close", log)
				}
			} else {
				if tc.claimFailed && !strings.Contains(log, "claim-attempt:") {
					t.Fatalf("close log = %q, claim failure case did not attempt claim", log)
				}
				if tc.claimFailed && strings.Contains(log, "claim:") {
					t.Fatalf("close log = %q, claim failure unexpectedly succeeded", log)
				}
				if strings.Contains(log, "update:") {
					t.Fatalf("close log = %q, failure path must not close a bead", log)
				}
				if strings.Contains(log, "ack\n") {
					t.Fatalf("close log = %q, failure path must not drain-ack", log)
				}
			}
		})
	}
}

// TestPolecatPreflightSearchesLedgerBeforeFiling pins the search-before-file
// contract in the polecat preflight step.
//
// Concurrent polecats all run a baseline against the same base branch, so they
// all observe the same pre-existing failure within seconds of each other. With
// no ledger search in front of the create, each one files its own bug: five
// duplicates inside three minutes from three polecats, and one duplicate that
// sat ready in the pool after its original had already merged, one sling away
// from dispatching a polecat to redo merged work.
func TestPolecatPreflightSearchesLedgerBeforeFiling(t *testing.T) {
	step := formulaStep(t, readFormula(t, "mol-polecat-base.toml"), "preflight-tests")

	searchAt := strings.Index(step, "gc bd list --title-contains")
	if searchAt < 0 {
		t.Fatal("preflight-tests must search the ledger with `gc bd list --title-contains` before filing a pre-existing-failure bead")
	}
	createAt := strings.Index(step, "gc bd create")
	if createAt < 0 {
		t.Fatal("preflight-tests must still describe how to file a pre-existing-failure bead")
	}
	if searchAt > createAt {
		t.Error("preflight-tests searches the ledger after `gc bd create`; the search must gate the create, not follow it")
	}

	// The original report is usually already claimed by the polecat or refinery
	// fixing it, so an open-only filter misses the very bead it should match.
	searchCmd := step[searchAt:createAt]
	if !strings.Contains(searchCmd, "in_progress") {
		t.Error("the dedupe lookup must include --status in_progress; the existing bead is frequently already claimed")
	}

	// A lookup that errors is not an all-clear. The refinery's earlier attempt at
	// this check invoked a flag that does not exist (`gc bd list --search`), so it
	// errored every run and the "no duplicate found" branch filed anyway.
	if !strings.Contains(step, "LOOKUP_RC") || !strings.Contains(step, `"$LOOKUP_RC" -ne 0`) {
		t.Error("the dedupe lookup must capture its exit status in LOOKUP_RC and fail closed on a non-zero result")
	}

	// Round-trip matchability: agents write different prose for one defect, so the
	// filed title has to carry the same stable key the next agent searches for.
	if !strings.Contains(searchCmd, `--title-contains "$SYMPTOM_KEY"`) {
		t.Error("the dedupe lookup must search by the stable $SYMPTOM_KEY, not by free-text description")
	}
	if !strings.Contains(step[createAt:], "$SYMPTOM_KEY") {
		t.Error("the filed title must embed $SYMPTOM_KEY verbatim so the next polecat's lookup matches it")
	}
}

// TestPolecatPreflightKeysOnTestFunctionNotSubtest pins the two widenings that
// an exact-title match alone does not deliver.
//
// Go subtests make the reported name unstable across agents: two concurrent
// polecats often fail different subtests of the same function
// (`TestX/clean_config_with_residual_files_is_a_conflict` versus
// `TestX/peer_successor_cross-device_tree`), search different strings, and both
// file. Stripping at the first `/` keys them together.
//
// Sibling functions in one package are the second gap: three different
// `TestDisableAndPurge*` functions can share a single root cause (for example
// an environment leak), and no exact-name key groups them. Eight
// productmetrics beads landed in ~38h that way.
func TestPolecatPreflightKeysOnTestFunctionNotSubtest(t *testing.T) {
	step := formulaStep(t, readFormula(t, "mol-polecat-base.toml"), "preflight-tests")

	if !strings.Contains(step, "%%/*") {
		t.Error("the symptom key must strip the Go subtest suffix (${NAME%%/*}) so sibling subtests of one function share a key")
	}

	familyAt := strings.Index(step, `--title-contains "$SYMPTOM_FAMILY"`)
	if familyAt < 0 {
		t.Error("preflight-tests must widen to a $SYMPTOM_FAMILY lookup; sibling tests in one package usually share one root cause")
	}
	createAt := strings.Index(step, "gc bd create")
	if createAt >= 0 && familyAt > createAt {
		t.Error("the family lookup must run before `gc bd create`, not after it")
	}

	// The family branch is a judgement call, so nothing auto-assigns the match —
	// but the step must still tell the agent how to hand a family hit to 3c, or
	// 3c's `gc bd comment "$EXISTING"` runs with an empty id.
	if !strings.Contains(step, `EXISTING="<the bead id you judged to be the same defect>"`) {
		t.Error("3b2 must show how to set $EXISTING for a family match; 3c cannot comment without it")
	}
	if !strings.Contains(step, "FAMILY_RC") {
		t.Error("the family lookup must capture its exit status; a failed lookup is not an all-clear")
	}
}

// TestPolecatPreflightChecksRecentlyClosedBeforeFiling pins the staleness gate.
//
// Re-filing a defect that already merged puts a ready bead in the pool, and the
// next sling dispatches a polecat to redo merged work.
func TestPolecatPreflightChecksRecentlyClosedBeforeFiling(t *testing.T) {
	step := formulaStep(t, readFormula(t, "mol-polecat-base.toml"), "preflight-tests")

	closedAt := strings.Index(step, "--closed-after")
	if closedAt < 0 {
		t.Fatal("preflight-tests must check recently-closed beads before filing; a stale baseline otherwise re-files merged work")
	}
	createAt := strings.Index(step, "gc bd create")
	if createAt >= 0 && closedAt > createAt {
		t.Error("the recently-closed check must run before `gc bd create`, not after it")
	}
	if !strings.Contains(step, "CLOSED_RC") {
		t.Error("the recently-closed lookup must capture its exit status; a failed lookup is not proof the fix has not landed")
	}
}

// TestPolecatSelfReviewDefersToPreflightDedupeProtocol keeps the second
// pre-existing-failure filing path pointed at the one protocol.
//
// self-review also tells the polecat to file a bead when a failure turns out to
// be pre-existing. Restating the protocol there would let the two copies drift;
// referring to preflight-tests keeps one definition.
func TestPolecatSelfReviewDefersToPreflightDedupeProtocol(t *testing.T) {
	step := formulaStep(t, readFormula(t, "mol-polecat-base.toml"), "self-review")

	if !strings.Contains(step, "preflight-tests") {
		t.Error("self-review's pre-existing-failure path must point at the preflight-tests search-first protocol instead of filing directly")
	}
	if strings.Contains(step, "gc bd create") {
		t.Error("self-review must not carry its own `gc bd create` for pre-existing failures; that bypasses the dedupe protocol")
	}
}

// TestCoreShippedAssetsAvoidNonexistentBDListSearchFlag guards the failure mode
// that made the sibling fix a no-op: `gc bd list` has no `--search` flag, so a
// dedupe lookup written against it exits 1 and returns nothing, which reads as
// "no duplicate exists" to the branch that follows. Search by --title-contains.
func TestCoreShippedAssetsAvoidNonexistentBDListSearchFlag(t *testing.T) {
	err := fs.WalkDir(PackFS, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(PackFS, path)
		if err != nil {
			return err
		}
		body := string(data)
		for _, line := range strings.Split(body, "\n") {
			if strings.Contains(line, "bd list") && strings.Contains(line, "--search") {
				t.Errorf("%s: `bd list --search` is not a real flag and exits 1; use --title-contains: %s", path, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking embedded core pack: %v", err)
	}
}

// TestMolPolecatCommitResolvesRepoBeforeRemovingWorktree pins the fix for a
// stranded-worktree bug: `git worktree remove` resolves the repo from cwd,
// and this step `cd`s away from the worktree before removing it, so the bare
// form exits 128 having unregistered nothing while `rm -rf` deletes the
// directory anyway, leaving the registration behind forever. These are
// bootstrap templates, so every city seeded by `gc city init` inherited the
// defect (ga-x1u5cr; contributing cause of ga-lc9yx's 396 dead worktrees).
func TestMolPolecatCommitResolvesRepoBeforeRemovingWorktree(t *testing.T) {
	step := formulaStep(t, readFormula(t, "mol-polecat-commit.toml"), "commit-and-push")

	if strings.Contains(step, `git worktree remove "$WORKTREE_PATH" --force`) {
		t.Error("commit-and-push calls bare `git worktree remove` after `cd ..`; resolve the repo via --git-common-dir first and remove via `git -C \"$REPO\" worktree remove`")
	}
	if !strings.Contains(step, "--git-common-dir") {
		t.Error("commit-and-push must resolve REPO via `git rev-parse --path-format=absolute --git-common-dir` before `cd ..`, so worktree removal does not depend on cwd")
	}
	if !strings.Contains(step, `git -C "$REPO" worktree remove`) {
		t.Error(`commit-and-push must remove the worktree via git -C "$REPO" worktree remove, not a bare invocation`)
	}

	// `git -C ""` is a no-op that silently resolves the repo from cwd, and this
	// step has already `cd ..`'d away from the worktree by then. An unresolved
	// REPO must short-circuit rather than degrade back to cwd-dependent removal.
	if !strings.Contains(step, `[ -z "$REPO" ]`) {
		t.Error(`commit-and-push must bail on an empty $REPO; git -C "" silently resolves from cwd, which is exactly the bug this step fixes`)
	}

	// WORKTREE_PATH is $(pwd), and `git worktree remove` exits 128 on a main
	// working tree. Without the guard the failure path rm -rf's the whole repo.
	guardAt := strings.Index(step, `[ -f "$WORKTREE_PATH/.git" ]`)
	if guardAt < 0 {
		t.Fatal(`commit-and-push must guard the rm -rf fallback with [ -f "$WORKTREE_PATH/.git" ]; a linked worktree's .git is a file, a main checkout's is a directory`)
	}
	// Match the delete command itself, not the word: the surrounding comment and
	// the refusal message both mention `rm -rf` and would otherwise be found first.
	if got := strings.Count(step, `rm -rf "$WORKTREE_PATH"`); got != 1 {
		t.Fatalf(`commit-and-push must delete the worktree exactly once behind the guard; found %d occurrences of rm -rf "$WORKTREE_PATH"`, got)
	}
	removeAt := strings.Index(step, `rm -rf "$WORKTREE_PATH"`)
	if guardAt > removeAt {
		t.Error("commit-and-push runs rm -rf before the linked-worktree check; the check must gate the delete, not follow it")
	}
}

// TestMolScopedWorkResolvesRepoBeforeRemovingWorktree pins the same fix for
// mol-scoped-work's cleanup step, which is worse than mol-polecat-commit's:
// its `|| rm -rf` fallback makes the stranded git registration the designed
// outcome of the bare form's failure path, not just an incidental risk.
func TestMolScopedWorkResolvesRepoBeforeRemovingWorktree(t *testing.T) {
	step := formulaStep(t, readFormula(t, "mol-scoped-work.toml"), "cleanup-worktree")

	if strings.Contains(step, `git worktree remove --force "$WORKTREE" || rm -rf "$WORKTREE"`) {
		t.Error("cleanup-worktree calls bare `git worktree remove --force ... || rm -rf`; resolve the repo via --git-common-dir first and remove via `git -C \"$REPO\" worktree remove`")
	}
	if !strings.Contains(step, "--git-common-dir") {
		t.Error(`cleanup-worktree must resolve REPO via git -C "$WORKTREE" rev-parse --path-format=absolute --git-common-dir; cwd is not guaranteed inside the repo at this step`)
	}
	if !strings.Contains(step, `git -C "$REPO" worktree remove`) {
		t.Error(`cleanup-worktree must remove the worktree via git -C "$REPO" worktree remove, not a bare invocation`)
	}

	// A stale directory that still passes [ -d ], or a git too old for
	// --path-format, leaves REPO empty; `git -C ""` then resolves from a cwd
	// this step explicitly does not guarantee is inside the repo.
	if !strings.Contains(step, `[ -z "$REPO" ]`) {
		t.Error(`cleanup-worktree must bail on an empty $REPO; git -C "" silently resolves from cwd, which this step cannot assume`)
	}

	guardAt := strings.Index(step, `[ -f "$WORKTREE/.git" ]`)
	if guardAt < 0 {
		t.Fatal(`cleanup-worktree must guard the rm -rf fallback with [ -f "$WORKTREE/.git" ]; a linked worktree's .git is a file, a main checkout's is a directory`)
	}
	if got := strings.Count(step, `rm -rf "$WORKTREE"`); got != 1 {
		t.Fatalf(`cleanup-worktree must delete the worktree exactly once behind the guard; found %d occurrences of rm -rf "$WORKTREE"`, got)
	}
	removeAt := strings.Index(step, `rm -rf "$WORKTREE"`)
	if guardAt > removeAt {
		t.Error("cleanup-worktree runs rm -rf before the linked-worktree check; the check must gate the delete, not follow it")
	}
}
