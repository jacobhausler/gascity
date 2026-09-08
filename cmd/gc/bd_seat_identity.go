package main

import (
	"os"
	"strings"

	"github.com/gastownhall/gascity/internal/bdflags"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// Seat-identity actor substitution for the seat-facing bd gates
// (gastownhall/gascity#5716 — the split #5663 left behind).
//
// #5663 moved the CLAIM assignee to the session bead ID (hookClaimAssignee-
// Identity's last fallback, cmd_hook.go) while bd kept comparing one string:
// its close authority, assignee-update fence, claim idempotence/refusal and
// heartbeat lease gates all test `assignee == actor`, where actor is the single
// BEADS_ACTOR this process carries — the session NAME for an unaliased pool
// seat. Two channels, so a seat acting on its own bead is refused by its own
// machinery: "unable to claim source work bead", "already claimed by", a
// heartbeat that cannot advance a lease granted under the other form.
//
// gc already answers "is this bead mine?" as a SET question.
// session.AssigneeIdentities enumerates the forms one session answers to and
// hookClaimHasIdentity is the membership test; every reconciler-side caller
// uses them (pool_session_name.go, assigned_work_scope.go,
// build_desired_state.go, idle_nudge.go), and the hook claim path already
// SUBSTITUTES the row's own assignee for the actor precisely because "bd's
// idempotent --claim path requires the actor to match the existing assignee
// exactly" (cmd_hook_claim.go). The seat-facing verbs were the last callers
// still reading one env string.
//
// This file moves that substitution to those verbs and does nothing else. The
// actor forwarded to bd becomes the bead's current assignee IFF that assignee
// is a member of THIS session's identity set and the actor we would otherwise
// forward is one too. The ordering is load-bearing: membership is proven BEFORE
// the substitution, which is what preserves the anti-theft property the bd
// guards exist for (be-035, bd-98s5c) and the #5663 property that a pool
// occupant cannot steal another occupant's claim — a bead held under a
// different seat's identity is never rewritten, so bd still refuses it.
//
// The real fix is bd taking an actor identity set (carried by a beads-side
// actor-identities input at cmd/bd's actor resolution). When that lands, DELETE
// this file and go back to forwarding the raw actor; that is why the
// substitution is confined to the verbs below and adds no label, flag, or
// sweep.

const bdSeatIdentityClaimFlag = "-" + "-claim"

// bdSeatIdentityVerbs are the bd subcommands whose store-level gates compare
// the calling actor against the bead's current assignee:
//
//	update --claim     claim CAS idempotence and refusal (issueops/claim.go)
//	update -a / -a ""  the anti-takeover assignee fence (validation/issue.go)
//	heartbeat          lease-holder UPDATE and no-lease fallback (issueops/lease.go)
//	close              close authority (validation/issue.go)
//
// `release-if-current` and `update --if-assignee` are deliberately absent: both
// CAS on a caller-supplied expected assignee and the actor never enters, so
// they are already identity-agnostic — which is why the release half of the
// worker ritual works while close does not. Rewriting anything there would only
// obscure who actually released the bead.
var bdSeatIdentityVerbs = map[string]bool{
	"update":    true,
	"heartbeat": true,
	"close":     true,
}

// bdSeatIdentityEnvKeys are the runtime-environment forms a live session
// answers to. They are the values internal/session.RuntimeEnvWithSessionContext
// writes for one session Info — the same values cmd_hook.go feeds its own
// IdentityCandidates — so the set built here cannot drift from the set the hook
// claim path already trusts.
//
// GC_TEMPLATE is deliberately NOT a candidate. It is the bare pool template,
// which the [[named_session]] holder answers to as well; admitting it would let
// a suffixed worker adopt the holder's in-progress bead — the ga-80pen8
// failure, which is why cmd_hook.go keeps the bare template in RouteTargets
// (fresh claims of UNASSIGNED routed work) and out of identity candidates.
// BEADS_ACTOR is likewise not a candidate here: it is the actor being tested,
// and admitting it would make its own membership check vacuous.
var bdSeatIdentityEnvKeys = []string{
	"GC_SESSION_ID",
	"GC_SESSION_NAME",
	"GC_ALIAS",
	"GC_AGENT",
}

// bdSeatIdentityActorOverride decides which actor a seat-facing bd mutation is
// forwarded under. ok=false means "forward the ambient actor exactly as today"
// — every arm fails closed toward today's behaviour, because the substitution
// only ever ADDS an acceptance bd would otherwise grant an identity-set member.
//
// fetched is the exact-ID guard's already-read beads; the override never opens
// a store or re-reads a bead, and refuses to guess at a bead it has not seen.
func bdSeatIdentityActorOverride(bdArgs []string, fetched map[string]beads.Bead, identities []string, rawActor string) (string, bool) {
	id, ok := bdSeatIdentityGatedWriteID(bdArgs)
	if !ok || len(identities) == 0 {
		return "", false
	}
	bead, found := fetched[id]
	if !found {
		return "", false
	}
	return bdSeatIdentitySubstitute(bead.Assignee, rawActor, identities)
}

// bdSeatIdentityGatedWriteID is the part of the decision that needs no store
// and no environment: does this argv address exactly one bead through a verb
// whose gate compares the actor against the assignee? It is exported to the
// door function so a plain `gc bd show` never pays for an identity read, and
// every refusal on this list is a return to today's behaviour.
func bdSeatIdentityGatedWriteID(bdArgs []string) (string, bool) {
	if len(bdArgs) == 0 {
		return "", false
	}
	verb, rest := bdflags.SplitGlobalFlags(bdArgs)
	if !bdSeatIdentityVerbs[verb] {
		return "", false
	}
	// An explicit --actor is the caller naming the identity they act as; bd
	// gives it precedence over the environment, and so does this decision.
	if bdSeatIdentityHasExplicitActor(bdArgs) {
		return "", false
	}
	if !bdSeatIdentityVerbIsGated(verb, rest) {
		return "", false
	}
	ids, ok, ambiguous := bdMutationWriteIDs(bdArgs)
	if !ok || ambiguous || len(ids) != 1 {
		// A batch write is refused rather than partially substituted: mixing one
		// bead's actor into another's would mis-attribute the mutation, and a
		// bd batch refuses the whole command on the first foreign bead anyway.
		return "", false
	}
	return ids[0], true
}

// bdSeatIdentitySubstitute is the membership decision both seat-facing doors
// share: the work-store passthrough and the class-store claim door. It returns
// the bead's current assignee when the assignee AND the actor we would forward
// are both identities of this same session, and nothing otherwise.
func bdSeatIdentitySubstitute(assignee, rawActor string, identities []string) (string, bool) {
	assignee = strings.TrimSpace(assignee)
	rawActor = strings.TrimSpace(rawActor)
	if assignee == "" || rawActor == "" || len(identities) == 0 {
		return "", false
	}
	// Already the exact string bd wants: nothing to repair, and rewriting an
	// actor that matches would only hide which form a claim actually used.
	if assignee == rawActor {
		return "", false
	}
	// The actor must be this session's own. A process whose actor belongs to
	// someone else (an order subprocess running as `order:<name>`, the
	// supervisor's `controller`, an operator's shell) is left exactly as
	// today's build behaves.
	if !hookClaimHasIdentity(rawActor, identities) {
		return "", false
	}
	// The bead must be held under a form of THIS session — not another seat's
	// session id, not a bare pool template, not an unrelated human. This is the
	// arm that keeps #5663's safety property: an occupant cannot take, nudge,
	// heartbeat, or close a bead another occupant holds.
	if !hookClaimHasIdentity(assignee, identities) {
		return "", false
	}
	return assignee, true
}

// bdSeatIdentityVerbIsGated reports whether this particular invocation actually
// reaches an actor-vs-assignee gate. Not every `update` does: a plain metadata
// write is not an ownership operation, and `--if-assignee` / `--force` carry
// their own authorization that this seam must not interfere with.
//
// An argument the scanner cannot classify exactly is refused, matching
// bdMutationWriteIDs' fail-closed contract.
func bdSeatIdentityVerbIsGated(verb string, rest []string) bool {
	switch verb {
	case "heartbeat":
		// rewriteBdHeartbeatArgs has already reduced the argv to the verb plus
		// one pre-validated id, and bd's heartbeat is always an ownership
		// operation on the caller's own lease.
		return true
	case "close":
		// `close --force` bypasses bd's authority check by design and records
		// the operator as the closer; do not silently rename that actor.
		present, ambiguous := bdSeatIdentityFlagsPresent(verb, rest, map[string]bool{"--force": true, "-f": true})
		if ambiguous || present["--force"] || present["-f"] {
			return false
		}
		return true
	case "update":
		// Only the two ownership spellings: taking a claim, and moving one.
		// `--if-assignee` is a caller-supplied CAS and does not need (and must
		// not get) a substituted actor.
		want := map[string]bool{bdSeatIdentityClaimFlag: true, "-a": true, "--assignee": true, "--if-assignee": true}
		present, ambiguous := bdSeatIdentityFlagsPresent(verb, rest, want)
		if ambiguous || present["--if-assignee"] {
			return false
		}
		return present[bdSeatIdentityClaimFlag] || present["-a"] || present["--assignee"]
	default:
		return false
	}
}

// bdSeatIdentityFlagsPresent reports which of `want` appear in a subcommand's
// argv, skipping the values of value-consuming flags (both subcommand-local and
// global) so a flag's VALUE is never read as a flag, and reporting ambiguity for
// any flag this package cannot classify.
func bdSeatIdentityFlagsPresent(sub string, rest []string, want map[string]bool) (map[string]bool, bool) {
	found := make(map[string]bool, len(want))
	valueFlags := bdSubcmdValueFlags(sub)
	boolFlags := bdSubcmdBoolFlags(sub)
	globalValue := bdflags.GlobalValueFlags()
	globalBool := bdflags.GlobalBoolFlags()
	positional := false
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		if positional {
			continue
		}
		if arg == "--" {
			positional = true
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name, inlineValue := arg, false
		if idx := strings.IndexByte(arg, '='); idx >= 0 {
			name, inlineValue = arg[:idx], true
		}
		if want[name] {
			found[name] = true
		}
		takesValue := valueFlags[name] || globalValue[name]
		if takesValue && !inlineValue {
			i++
			continue
		}
		if !takesValue && !boolFlags[name] && !globalBool[name] && !globalValue[name] {
			// Unknown flag: it might consume the token this scan is reading as
			// a flag or an id. Fail closed.
			return nil, true
		}
	}
	return found, false
}

// bdSeatIdentityHasExplicitActor reports an `--actor` on the invocation, in
// either spelling.
func bdSeatIdentityHasExplicitActor(bdArgs []string) bool {
	for _, arg := range bdArgs {
		if arg == "--actor" || strings.HasPrefix(arg, "--actor=") {
			return true
		}
	}
	return false
}

// bdSeatIdentityCandidatesFromEnv builds the current session's assignee-identity
// set from its own runtime environment, in the same shape and with the same
// de-duplication the hook claim path uses (hookClaimIdentityCandidates, which
// also carries the legacy workflow-control spelling of a name).
func bdSeatIdentityCandidatesFromEnv(env []string) []string {
	values := make([]string, 0, len(bdSeatIdentityEnvKeys))
	for _, key := range bdSeatIdentityEnvKeys {
		values = append(values, envValueFromList(env, key))
	}
	return hookClaimIdentityCandidates(values...)
}

// bdSeatIdentityIdentitiesFor returns the session's identity set for a CLI
// one-shot: the environment's forms, plus every form the session bead records.
// `configured_named_identity` and `alias_history` exist only on the bead, and
// `AssigneeIdentities` is the shipped codec that enumerates them, so the set
// this returns is the same one the reconciler and the API assignee filter use.
//
// Best-effort by design: a nil front door, an absent session bead, or a store
// that cannot answer leaves the environment set standing, which is the set the
// hook claim path has always used. A failed identity read may narrow what is
// accepted; it must never widen it.
func bdSeatIdentityIdentitiesFor(env []string, sessFront *sessionpkg.Store) []string {
	identities := bdSeatIdentityCandidatesFromEnv(env)
	if sessFront == nil {
		return identities
	}
	info, found, err := lookupSessionBeadByIDInfo(sessFront, envValueFromList(env, "GC_SESSION_ID"))
	if err != nil || !found {
		return identities
	}
	merged := make([]string, 0, len(identities)+6)
	merged = append(merged, identities...)
	merged = append(merged, sessionpkg.AssigneeIdentities(info)...)
	return hookClaimIdentityCandidates(merged...)
}

// bdSeatIdentityOverride is the door doBd calls: it reads this process's own
// identity set, asks the decision, and reports the actor the passthrough should
// carry. It returns ok=false whenever the exact-ID guard could not read the
// subject bead, so the substitution never rests on an unread bead.
func bdSeatIdentityOverride(bdArgs []string, fetched map[string]beads.Bead, store beads.Store, cfg *config.City, cityPath string) (string, bool) {
	id, ok := bdSeatIdentityGatedWriteID(bdArgs)
	if !ok {
		return "", false
	}
	if _, found := fetched[id]; !found {
		return "", false
	}
	env := os.Environ()
	var sessFront *sessionpkg.Store
	if store != nil {
		sessFront = sessionFrontDoor(cliSessionStore(store, cfg, cityPath))
	}
	return bdSeatIdentityActorOverride(bdArgs, fetched, bdSeatIdentityIdentitiesFor(env, sessFront), envValueFromList(env, "BEADS_ACTOR"))
}

// bdSeatIdentityWithActor returns env with BEADS_ACTOR set to actor, replacing
// every existing entry so the subprocess cannot see two values for it.
func bdSeatIdentityWithActor(env []string, actor string) []string {
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, entry := range env {
		if strings.HasPrefix(entry, "BEADS_ACTOR=") {
			if !replaced {
				out = append(out, "BEADS_ACTOR="+actor)
				replaced = true
			}
			continue
		}
		out = append(out, entry)
	}
	if !replaced {
		out = append(out, "BEADS_ACTOR="+actor)
	}
	return out
}

// envValueFromList returns the last value of key in a KEY=VALUE environment
// slice (the last wins where a slice carries duplicates), or "".
func envValueFromList(env []string, key string) string {
	value := ""
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			value = entry[len(prefix):]
		}
	}
	return strings.TrimSpace(value)
}

// bdByIDClaimActorFor is the class-store door (gate #12): the identity a routed
// claim of a class-owned bead acquires it for. A same-owner reclaim is a true
// no-op under the graph contract, so claiming under the bead's own assignee
// makes a retry idempotent for a seat whose actor arrives on the other channel
// — while a bead held by a different session keeps claiming under this
// process's own actor, where the contract reports a conflict.
//
// The identity set here is the environment's: this door is reached without a
// work store open, and a session-bead read is not worth adding to a claim path
// that already has one. #5716's upstream shape removes the difference.
func bdByIDClaimActorFor(currentAssignee string) string {
	actor := bdByIDClaimActor()
	if substituted, ok := bdSeatIdentitySubstitute(currentAssignee, actor, bdSeatIdentityCandidatesFromEnv(os.Environ())); ok {
		return substituted
	}
	return actor
}
