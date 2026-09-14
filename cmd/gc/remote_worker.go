package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/gastownhall/gascity/internal/api"
	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// This file is the remote leg of the worker commands: `gc hook --claim`,
// `gc hook current`, and the four-verb `gc bd` subset a worker lives on
// (show / update / comment / close). It exists so a session whose box is not
// the controller's host can do a worker's job.
//
// The rule the rest of the CLI already follows applies here unchanged: a
// command is remote-capable only if it resolves through
// resolveContextAllowRemote and routes through the remote transport. Nothing
// below can fall back to a local store, and nothing below runs when no remote
// context is configured — the local paths are reached by the same code they
// always were, byte for byte.
//
// What deliberately does NOT cross the wire is the shell `work_query`. It is a
// host-side command that reads a local store; running it in a box that has no
// store is the very problem this path exists to solve. The remote candidate
// scan asks the city for the beads routed to this session's identities
// instead, and every claim it then attempts is decided by the server's
// compare-and-swap, not by the scan.

// resolveWorkerTarget resolves the worker commands' target. It returns a
// mutation-capable remote client when a remote context is active, and
// (nil, false, nil) for a local target so the caller runs its existing local
// path untouched.
//
// A resolution failure with no remote selector present is not this function's
// to report. The resolver's local leg fails for local reasons — no city here,
// an unreadable city — and those errors belong to the local path, worded the
// way that path has always worded them. Reporting them from here would change
// the diagnostic every worker command gives when it is not remote at all.
func resolveWorkerTarget() (*api.Client, bool, error) {
	client, isRemote, _, err := resolveWriteTarget()
	if err != nil {
		if !readRemoteSelection().hasExplicitRemote() {
			return nil, false, nil
		}
		return nil, isRemote, err
	}
	return client, isRemote, nil
}

// remoteWorkerSessionID reads the calling session's identity. Every worker
// operation is scoped to it, and a box that cannot name its session cannot be
// fenced, so an absent value is refused rather than defaulted.
func remoteWorkerSessionID() (string, error) {
	id := strings.TrimSpace(os.Getenv("GC_SESSION_ID"))
	if id == "" {
		return "", errors.New("no session identity (set $GC_SESSION_ID); a remote worker command is always scoped to its own session")
	}
	return id, nil
}

// remoteWorkerIdentities returns the identities this session may claim under,
// most specific first. It mirrors the local assignee resolution
// (alias, then agent, then session name, then session id) but reads them from
// the environment only: the local path derives them from city config this box
// does not have.
func remoteWorkerIdentities(sessionID string) []string {
	var out []string
	add := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || slices.Contains(out, candidate) {
			return
		}
		out = append(out, candidate)
	}
	for _, candidate := range []string{
		strings.TrimSpace(os.Getenv("GC_ALIAS")),
		strings.TrimSpace(os.Getenv("GC_AGENT")),
		strings.TrimSpace(os.Getenv("GC_SESSION_NAME")),
		sessionID,
	} {
		add(candidate)
	}
	template := strings.TrimSpace(os.Getenv("GC_TEMPLATE"))
	if template != "" {
		// GC_TEMPLATE is commonly bare inside a rig. Recover the qualified
		// route from the canonical runtime identity projection, including the
		// fallback that derives a rig from a qualified alias or agent when the
		// provider omitted GC_RIG.
		rig := agentScriptRig()
		if rig != "" && !strings.Contains(template, "/") {
			add(rig + "/" + template)
		}
		add(template)
	}
	return out
}

// remoteHookCurrent is the remote leg of `gc hook current`. It preserves the
// local exit contract exactly: an unclaimed session exits 1 with a diagnostic
// rather than 0 with empty output, because a formula step that cannot name its
// own bead must fail loudly instead of letting a caller substitute "" and skip
// the close it owes.
func remoteHookCurrent(client *api.Client, sessionID string, idOnly bool, stdout, stderr io.Writer) int {
	beadID, err := client.WorkerCurrent(sessionID)
	if err != nil {
		fmt.Fprintf(stderr, "gc hook current: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if strings.TrimSpace(beadID) == "" {
		fmt.Fprintf(stderr, "gc hook current: session %s has no current claim (nothing claimed through gc hook --claim)\n", sessionID) //nolint:errcheck
		return 1
	}
	if idOnly {
		fmt.Fprintln(stdout, beadID) //nolint:errcheck // best-effort stdout
		return 0
	}
	fmt.Fprintf(stdout, "%s (claimed by session %s)\n", beadID, sessionID) //nolint:errcheck
	return 0
}

// remoteHookDrainAck is the remote leg of `gc hook --drain-ack` WITHOUT
// --claim: the claim-free ack. It resolves the calling session, posts the drain
// acknowledgement, and returns. It never enters the claim protocol, never reads
// the work query, and cannot come back holding a bead.
//
// That last property is the whole point. `--claim --drain-ack` acknowledges the
// drain only on a NO-WORK result, so a finished one_shot seat calling it picks
// up another bead whenever the pool has work and then exits holding it - the
// claim path does not consult the agent's lifecycle. A seat on its way out
// needs a door that can only say "I am done".
func remoteHookDrainAck(client *api.Client, opts hookCommandOptions, stdout, stderr io.Writer) int {
	sessionID, err := remoteWorkerSessionID()
	if err != nil {
		fmt.Fprintf(stderr, "gc hook --drain-ack: %v\n", err) //nolint:errcheck
		return 1
	}
	if err := client.WorkerDrainAck(sessionID); err != nil {
		fmt.Fprintf(stderr, "gc hook --drain-ack: %v\n", err) //nolint:errcheck
		return 1
	}
	if opts.JSON {
		fmt.Fprintf(stdout, "{\"action\":\"drain\",\"reason\":\"drain_ack\",\"drain_ack\":true,\"session\":%q}\n", sessionID) //nolint:errcheck
		return 0
	}
	fmt.Fprintf(stdout, "drain acknowledged (session %s)\n", sessionID) //nolint:errcheck
	return 0
}

// remoteHookClaim is the remote leg of `gc hook --claim`. It emits the same
// result schema the local path emits — the consumer is a startup wrapper that
// cannot tell which leg produced its line, and must not have to.
//
// Order of preference matches the local protocol: work this session already
// holds is reported before any fresh claim is attempted, so a restarted seat
// resumes its own bead instead of racing for a second one.
func remoteHookClaim(client *api.Client, opts hookCommandOptions, stdout, stderr io.Writer) int {
	sessionID, err := remoteWorkerSessionID()
	if err != nil {
		fmt.Fprintf(stderr, "gc hook --claim: %v\n", err) //nolint:errcheck
		return 1
	}
	identities := remoteWorkerIdentities(sessionID)
	assignee := identities[0]

	drainAck := func(io.Writer) error { return client.WorkerDrainAck(sessionID) }

	// A claim this session already holds is reported as-is, never re-claimed:
	// the store's claim is idempotent for the current holder, but the route
	// refuses a second claim with a 409 because the pointer already names live
	// work — and reporting is the whole job here.
	//
	// The pointer read here is also the snapshot every claim below fences its
	// compare-and-swap against, so a pointer that moves under this invocation
	// loses rather than overwriting its successor.
	observed := ""
	if held, err := client.WorkerCurrent(sessionID); err == nil && strings.TrimSpace(held) != "" {
		observed = strings.TrimSpace(held)
		bead, getErr := client.GetBead(strings.TrimSpace(held))
		if getErr == nil && strings.EqualFold(strings.TrimSpace(bead.Body.Status), "in_progress") {
			return writeRemoteHookClaimResult(client, sessionID, hookClaimJSONResult{
				SchemaVersion: "1",
				OK:            true,
				Command:       hookClaimCommandName,
				Action:        "work",
				Reason:        "existing_assignment",
				BeadID:        bead.Body.ID,
				Assignee:      bead.Body.Assignee,
				Route:         hookClaimRoute(bead.Body),
			}, false, opts, stdout, stderr)
		}
	}

	candidates, claimsErrored := remoteHookCandidates(client, identities, stderr)
	for _, candidate := range candidates {
		claimResult, err := client.WorkerClaim(context.Background(), api.WorkerVerbRequest{
			SessionID:    sessionID,
			Assignee:     assignee,
			BeadID:       candidate.ID,
			CurrentClaim: observed,
		})
		switch {
		case err != nil && func() bool {
			code, ok := api.WorkerVerbStatusCode(err)
			return ok && code == http.StatusConflict
		}():
			// Someone else won, or this session already holds different work.
			// Both are ordinary race outcomes; try the next candidate.
			continue
		case err != nil:
			// An operational write failure must not wedge the hook. Record it so
			// the drain below reports claims_errored rather than a healthy
			// no_work, and move on — the work is reclaimed next tick either way.
			fmt.Fprintf(stderr, "gc hook --claim: skipping %s: %v\n", candidate.ID, err) //nolint:errcheck
			claimsErrored = true
			continue
		}
		claimed := claimResult.Bead
		result := hookClaimJSONResult{
			SchemaVersion: "1",
			OK:            true,
			Command:       hookClaimCommandName,
			Action:        "work",
			Reason:        "claimed",
			BeadID:        firstNonEmptyHookValue(claimed.ID, candidate.ID),
			Assignee:      firstNonEmptyHookValue(claimed.Assignee, assignee),
			Route:         hookClaimRoute(claimed),
		}
		return writeRemoteHookClaimResult(client, sessionID, result, true, opts, stdout, stderr)
	}

	reason := hookClaimReasonNoWork
	if claimsErrored {
		reason = hookClaimReasonClaimsErrored
	}
	return writeHookClaimDrain(hookClaimLabel, reason, opts.JSON, opts.DrainAck, drainAck, stdout, stderr)
}

// remoteHookCandidates asks the city for the open work this session may claim
// and returns it in identity order — most specific identity first — so a
// suffixed pool worker prefers work routed to it over work routed to its bare
// template.
//
// It lists open beads once and selects locally, because the city ROUTES work
// with the gc.routed_to metadata key and leaves assignee null. The earlier
// `?assignee=<identity>` list matched the assignee COLUMN
// (internal/api/huma_handlers_beads.go -> ListByAssignee), so routed work was
// invisible to it and a remote worker saw no work at all while the local leg —
// which reads gc.routed_to through the generated work_query — saw the same
// beads fine (DF-08 L2 gap 2). The predicate here is the local leg's own
// hookClaimMatchesRoute, so the two lanes cannot drift apart.
//
// A list failure is reported and recorded rather than fatal, so the caller
// drains with claims_errored instead of a healthy no_work. The single
// unfiltered list is also strictly fewer requests than the per-identity fan-out
// it replaces; the server has no metadata filter to push the selection into.
func remoteHookCandidates(client *api.Client, identities []string, stderr io.Writer) ([]beads.Bead, bool) {
	listed, err := client.ListBeads(api.ListBeadsOpts{Status: "open"})
	if err != nil {
		fmt.Fprintf(stderr, "gc hook --claim: listing open work: %v\n", err) //nolint:errcheck
		return nil, true
	}
	var out []beads.Bead
	seen := map[string]bool{}
	for _, identity := range identities {
		identity = strings.TrimSpace(identity)
		if identity == "" {
			continue
		}
		for _, b := range listed.Body {
			if seen[b.ID] || strings.TrimSpace(b.ID) == "" {
				continue
			}
			// Mail is read, not claimed: a message bead addressed to this
			// identity has the same shape as routed work and would otherwise be
			// returned ahead of it.
			if strings.EqualFold(strings.TrimSpace(b.Type), "message") {
				continue
			}
			if !remoteHookCandidateForIdentity(b, identity) {
				continue
			}
			seen[b.ID] = true
			out = append(out, b)
		}
	}
	return out, false
}

// remoteHookCandidateForIdentity reports whether an open bead is claimable by
// identity: work already assigned to it (the direct-assignment tier), or work
// routed to it with the assignee still empty (the routed tier, which is how
// `gc sling` leaves a bead). A bead held by somebody else is neither.
func remoteHookCandidateForIdentity(b beads.Bead, identity string) bool {
	assignee := strings.TrimSpace(b.Assignee)
	if assignee == identity {
		return true
	}
	if assignee != "" {
		return false
	}
	return hookClaimMatchesRoute(b, []string{identity})
}

// writeRemoteHookClaimResult writes the one line that carries a claim result to
// its consumer and, when that write fails on a claim this invocation minted,
// gives the claim back instead of parking it. A closed stdout pipe is the
// signature of an orphaned tool call: nobody will execute a claim whose result
// never left the process.
func writeRemoteHookClaimResult(client *api.Client, sessionID string, result hookClaimJSONResult, minted bool, opts hookCommandOptions, stdout, stderr io.Writer) int {
	if err := writeHookClaimResultLine(result, opts.JSON, stdout); err != nil {
		fmt.Fprintf(stderr, "gc hook --claim: writing result for %s: %v\n", result.BeadID, err) //nolint:errcheck
		if !minted {
			return 1
		}
		releaseResult, relErr := client.WorkerRelease(context.Background(), api.WorkerVerbRequest{
			SessionID: sessionID,
			Assignee:  result.Assignee,
			BeadID:    result.BeadID,
		})
		switch {
		case relErr != nil:
			fmt.Fprintf(stderr, "gc hook --claim: releasing undelivered claim %s: %v\n", result.BeadID, relErr) //nolint:errcheck
		case releaseResult.Skipped:
			fmt.Fprintf(stderr, "gc hook --claim: undelivered claim %s was no longer ours to release\n", result.BeadID) //nolint:errcheck
		}
		return 1
	}
	return 0
}

// remoteWorkerBdVerbs is the pre-existing non-lifecycle subset of `gc bd`.
// Lifecycle verbs are intercepted by routeBdWorkerRemote before this legacy
// path, so an untyped close can never be sent by doBd to a remote city.
var remoteWorkerBdVerbs = map[string]bool{
	"show":    true,
	"update":  true,
	"comment": true,
}

// remoteBdFlagArity is the accept-list for the worker `gc bd` subset: for each
// verb, the flags this leg can express over the wire and how many arguments each
// consumes after itself. A flag absent from its verb's table is REFUSED, never
// dropped — a command that half-applies is worse than one that fails, and the
// operator asked for something the wire form cannot say.
//
// The tables are also what lets flags appear before the bead id, the way bd
// itself accepts them: the parser can only tell `--status open mc-7` from
// `--json mc-7` by knowing which flag takes a value.
var remoteBdFlagArity = map[string]map[string]int{
	"show":    {"--json": 0},
	"close":   {},
	"comment": {},
	"update": {
		"-s": 1, "--status": 1,
		"-d": 1, "--description": 1,
		"-t": 1, "--title": 1,
		"-a": 1, "--assignee": 1,
		"--set-metadata": 1,
	},
}

// remoteBdAccepts renders what a verb will take, for the refusal message. A verb
// with no expressible flags says so rather than printing an empty list.
func remoteBdAccepts(verb string) string {
	arity := remoteBdFlagArity[verb]
	if len(arity) == 0 {
		return "gc bd " + verb + " accepts no flags against a remote city"
	}
	names := make([]string, 0, len(arity))
	for name := range arity {
		if strings.HasPrefix(name, "--") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return "the remote worker subset accepts " + strings.Join(names, ", ") + " on gc bd " + verb
}

// splitRemoteBdArgs separates a worker `gc bd` invocation into its bead id, its
// flags (normalized to name-then-value pairs for the flags that take one), and
// the verb's remaining operands.
//
// Flags may precede the bead id, because bd accepts them there and an operator
// who types `gc bd show --json mc-7` against a remote city should not get "a
// bead id is required" for an invocation that names one.
//
// comment is the exception to flag parsing after the id: everything past the
// bead id is comment text, verbatim, including a word that starts with a dash.
func splitRemoteBdArgs(verb string, args []string) (id string, flags, operands []string, err error) {
	arity := remoteBdFlagArity[verb]
	textTail := verb == "comment"
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if textTail && id != "" {
			operands = append(operands, arg)
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			if id != "" {
				return "", nil, nil, fmt.Errorf("unexpected argument %q after bead id %s", arg, id)
			}
			id = arg
			continue
		}
		name, inline, hasInline := strings.Cut(arg, "=")
		takes, known := arity[name]
		if !known {
			return "", nil, nil, fmt.Errorf("%q is not supported against a remote city; %s", name, remoteBdAccepts(verb))
		}
		if takes == 0 {
			if hasInline {
				return "", nil, nil, fmt.Errorf("%s takes no value", name)
			}
			flags = append(flags, name)
			continue
		}
		if hasInline {
			flags = append(flags, name, inline)
			continue
		}
		if i+1 >= len(args) {
			return "", nil, nil, fmt.Errorf("%s requires a value", name)
		}
		i++
		flags = append(flags, name, args[i])
	}
	return id, flags, operands, nil
}

// remoteBd routes the worker `gc bd` subset to the API. It reports handled=false
// for anything outside that subset so the caller falls through to the existing
// remote refusal, which names the limitation rather than guessing.
func remoteBd(client *api.Client, args []string, stdout, stderr io.Writer) (code int, handled bool) {
	if len(args) == 0 {
		return 0, false
	}
	verb := strings.TrimSpace(args[0])
	if !remoteWorkerBdVerbs[verb] {
		return 0, false
	}
	id, flags, operands, err := splitRemoteBdArgs(verb, args[1:])
	if err != nil {
		fmt.Fprintf(stderr, "gc bd %s: %v\n", verb, err) //nolint:errcheck
		return 1, true
	}
	if id == "" {
		fmt.Fprintf(stderr, "gc bd %s: a bead id is required against a remote city\n", verb) //nolint:errcheck
		return 1, true
	}
	switch verb {
	case "show":
		return remoteBdShow(client, id, flags, stdout, stderr), true
	case "comment":
		text := strings.TrimSpace(strings.Join(operands, " "))
		if text == "" {
			fmt.Fprintln(stderr, "gc bd comment: comment text is required against a remote city (--stdin and --file are local-only)") //nolint:errcheck
			return 1, true
		}
		if err := client.WorkerCommentBead(id, text); err != nil {
			fmt.Fprintf(stderr, "gc bd comment: %v\n", err) //nolint:errcheck
			return 1, true
		}
		fmt.Fprintf(stdout, "commented on %s\n", id) //nolint:errcheck
		return 0, true
	default: // update
		return remoteBdUpdate(client, id, flags, stdout, stderr), true
	}
}

// remoteBdShow prints the bead the city holds. The local path hands the
// command to bd and prints bd's rendering; there is no bd here, so the wire
// form is what gets printed — JSON when asked for, and the fields a worker
// reads otherwise.
func remoteBdShow(client *api.Client, id string, flags []string, stdout, stderr io.Writer) int {
	read, err := client.GetBead(id)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd show: %v\n", err) //nolint:errcheck
		return 1
	}
	if slices.Contains(flags, "--json") {
		encoded, encErr := json.Marshal(read.Body)
		if encErr != nil {
			fmt.Fprintf(stderr, "gc bd show: %v\n", encErr) //nolint:errcheck
			return 1
		}
		fmt.Fprintln(stdout, string(encoded)) //nolint:errcheck
		return 0
	}
	b := read.Body
	fmt.Fprintf(stdout, "%s  %s\n", b.ID, b.Title)    //nolint:errcheck
	fmt.Fprintf(stdout, "status: %s\n", b.Status)     //nolint:errcheck
	fmt.Fprintf(stdout, "assignee: %s\n", b.Assignee) //nolint:errcheck
	if strings.TrimSpace(b.Description) != "" {
		fmt.Fprintf(stdout, "\n%s\n", b.Description) //nolint:errcheck
	}
	return 0
}

// remoteBdUpdate applies the field updates a worker makes, from the name/value
// pairs splitRemoteBdArgs already vetted against remoteBdFlagArity — which is
// where an unrecognized flag is refused rather than dropped, so an update that
// cannot be expressed over the wire fails loudly instead of half-applying.
func remoteBdUpdate(client *api.Client, id string, flags []string, stdout, stderr io.Writer) int {
	opts := api.UpdateBeadOpts{}
	metadata := map[string]string{}
	for i := 0; i+1 < len(flags); i += 2 {
		value := flags[i+1]
		switch flags[i] {
		case "-s", "--status":
			opts.Status = &value
		case "-d", "--description":
			opts.Description = &value
		case "-t", "--title":
			opts.Title = &value
		case "-a", "--assignee":
			opts.Assignee = &value
		case "--set-metadata":
			key, metaValue, ok := strings.Cut(value, "=")
			key = strings.TrimSpace(key)
			if !ok || key == "" {
				fmt.Fprintf(stderr, "gc bd update: metadata %q must be key=value\n", value) //nolint:errcheck
				return 1
			}
			if err := validateRemoteStepMetadata(key, metaValue); err != nil {
				fmt.Fprintf(stderr, "gc bd update: %v\n", err) //nolint:errcheck
				return 1
			}
			metadata[key] = metaValue
		default:
			// Unreachable while this switch and remoteBdFlagArity["update"] name
			// the same flags. It stays because they are two lists, and a flag
			// added to one and not the other must fail rather than be dropped.
			fmt.Fprintf(stderr, "gc bd update: %q is not supported against a remote city; %s\n", flags[i], remoteBdAccepts("update")) //nolint:errcheck
			return 1
		}
	}
	if len(metadata) > 0 {
		opts.Metadata = metadata
	}
	if err := client.UpdateBead(id, opts); err != nil {
		fmt.Fprintf(stderr, "gc bd update: %v\n", err) //nolint:errcheck
		return 1
	}
	fmt.Fprintf(stdout, "updated %s\n", id) //nolint:errcheck
	return 0
}

// remoteReceiptMetadataKey names the proof-of-work key a worker stamps on the
// bead it is closing. The key is declared in internal/beadmeta beside its
// work-record sibling WorkVerificationMetadataKey, so the vocabulary has one
// home; the vocabulary of its VALUE is the operator's, and gc only ever stores
// and echoes it. It is on the remote allow
// list because the box seat cannot write the row any other way, and a receipt
// the seat cannot record is a receipt that does not exist.
const remoteReceiptMetadataKey = beadmeta.ReceiptMetadataKey

// validateRemoteStepMetadata is deliberately an allowlist. The remote worker
// update route is a narrow formula-step completion leg, not a general metadata
// tunnel: accepting arbitrary keys would let a worker write control-plane
// state that this route does not own. The keys below are the vocabulary a
// worker legitimately owns — the step-completion triple, and the work-record
// keys the prestage and typed-close gates read back off the same row the worker
// is already entitled to write. Control-plane keys (gc.routed_to, gc.session_*,
// the lease keys) stay refused.
func validateRemoteStepMetadata(key, value string) error {
	switch key {
	case beadmeta.OutcomeMetadataKey:
		if value != "pass" && value != "fail" {
			return fmt.Errorf("metadata %q must be pass or fail, got %q", key, value)
		}
	case beadmeta.StepIDMetadataKey, beadmeta.StepRefMetadataKey, beadmeta.StepTimeoutMetadataKey,
		// A worker reports what it ran and what it produced; both are read from
		// the bead by the gates that release its work.
		beadmeta.WorkVerificationMetadataKey, beadmeta.OutputJSONMetadataKey,
		remoteReceiptMetadataKey:
	default:
		return fmt.Errorf("metadata key %q is not supported against a remote city; remote worker updates accept gc.outcome, the gc.step_* fields, gc.work_verification, gc.output_json and gc.receipt", key)
	}
	return nil
}
