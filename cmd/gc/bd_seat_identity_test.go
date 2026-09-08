package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// The fixture pair from cr-zt27m.4.56.1's audit: an UNALIASED pool seat, the
// measured shape of gastownhall/gascity#5716. #5663 put the session bead ID in
// the bead's assignee while BEADS_ACTOR stayed the slot label, so every
// single-string gate in bd refuses the seat's own verbs.
const (
	seatIDSess   = "gcg-session-X"
	seatNamePool = "mechanic-1-pool"
	otherSessID  = "gcg-session-Y"
)

func seatIdentities() []string {
	return []string{seatIDSess, seatNamePool}
}

// TestBDSeatIdentityNameVsID covers the audit's T1-T5 matrix on every changed
// verb: an identity arriving on the other channel is accepted, and an unrelated
// actor is still refused.
func TestBDSeatIdentityNameVsID(t *testing.T) {
	const id = "cr-abc.1"
	tests := []struct {
		name       string
		args       []string
		assignee   string
		actor      string
		identities []string
		want       string
		wantOK     bool
	}{
		{
			// T1 — the defect. Assignee is the session bead ID, actor is the
			// pool slot label; bd refuses this today.
			name:       "T1 claim: assignee=session id, actor=session name",
			args:       []string{"update", id, "--claim"},
			assignee:   seatIDSess,
			actor:      seatNamePool,
			identities: seatIdentities(),
			want:       seatIDSess,
			wantOK:     true,
		},
		{
			name:       "T1 heartbeat: lease holder=session id, actor=session name",
			args:       []string{"heartbeat", id},
			assignee:   seatIDSess,
			actor:      seatNamePool,
			identities: seatIdentities(),
			want:       seatIDSess,
			wantOK:     true,
		},
		{
			name:       "T1 close: closer's other identity holds the bead",
			args:       []string{"close", id, "--reason-file", "/tmp/reason.md"},
			assignee:   seatIDSess,
			actor:      seatNamePool,
			identities: seatIdentities(),
			want:       seatIDSess,
			wantOK:     true,
		},
		{
			name:       "T1 release: update -a \"\" off a session-id assignment",
			args:       []string{"update", id, "-a", ""},
			assignee:   seatIDSess,
			actor:      seatNamePool,
			identities: seatIdentities(),
			want:       seatIDSess,
			wantOK:     true,
		},
		{
			name:       "T1 release long form: --assignee",
			args:       []string{"update", id, "--assignee", ""},
			assignee:   seatIDSess,
			actor:      seatNamePool,
			identities: seatIdentities(),
			want:       seatIDSess,
			wantOK:     true,
		},
		{
			// T2 — the aliased shape already works; it must not change.
			name:       "T2 both channels agree (no substitution needed)",
			args:       []string{"update", id, "--claim"},
			assignee:   seatNamePool,
			actor:      seatNamePool,
			identities: seatIdentities(),
		},
		{
			// T3 — idempotence under the session-id spelling.
			name:       "T3 actor already names the assignee exactly",
			args:       []string{"update", id, "--claim"},
			assignee:   seatIDSess,
			actor:      seatIDSess,
			identities: seatIdentities(),
		},
		{
			// T4 — the safety property: another occupant's claim stays refused.
			name:       "T4 another seat's session id is never adopted",
			args:       []string{"update", id, "--claim"},
			assignee:   otherSessID,
			actor:      seatNamePool,
			identities: seatIdentities(),
		},
		{
			name:       "T4 close a bead held by another seat stays refused",
			args:       []string{"close", id},
			assignee:   otherSessID,
			actor:      seatNamePool,
			identities: seatIdentities(),
		},
		{
			// T5 — the alias_history arm of session.AssigneeIdentities.
			name:       "T5 assignee is a prior alias",
			args:       []string{"update", id, "--claim"},
			assignee:   "old-nux",
			actor:      "nux",
			identities: []string{seatIDSess, "rig--worker-3", "nux", "old-nux"},
			want:       "old-nux",
			wantOK:     true,
		},
		{
			// ga-80pen8: the bare pool template is a ROUTE target, not this
			// session's identity, so the [[named_session]] holder's bead stays
			// unadoptable by a suffixed worker.
			name:       "bare pool template assignment is not this seat's identity",
			args:       []string{"update", id, "--claim"},
			assignee:   "mechanic",
			actor:      seatNamePool,
			identities: seatIdentities(),
		},
		{
			// A process whose actor is someone else's (an order subprocess, the
			// supervisor, an operator shell) keeps today's behavior exactly.
			name:       "foreign actor is never rewritten",
			args:       []string{"close", id},
			assignee:   seatIDSess,
			actor:      "controller",
			identities: seatIdentities(),
		},
		{
			name:       "unassigned bead needs no substitution",
			args:       []string{"update", id, "--claim"},
			assignee:   "",
			actor:      seatNamePool,
			identities: seatIdentities(),
		},
		{
			name:       "empty identity set refuses everything",
			args:       []string{"update", id, "--claim"},
			assignee:   seatIDSess,
			actor:      seatNamePool,
			identities: nil,
		},
		{
			// A plain metadata write is not an ownership gate; leaving it alone
			// keeps the recorded actor honest on the writes that do succeed.
			name:       "plain update (no claim, no assignee) is not gated",
			args:       []string{"update", id, "--set-metadata", "gc.last_heartbeat_at=now"},
			assignee:   seatIDSess,
			actor:      seatNamePool,
			identities: seatIdentities(),
		},
		{
			name:       "update --if-assignee carries its own CAS",
			args:       []string{"update", id, "--if-assignee", seatIDSess, "-a", ""},
			assignee:   seatIDSess,
			actor:      seatNamePool,
			identities: seatIdentities(),
		},
		{
			name:       "close --force bypasses the authority gate by design",
			args:       []string{"close", id, "--force"},
			assignee:   seatIDSess,
			actor:      seatNamePool,
			identities: seatIdentities(),
		},
		{
			name:       "explicit --actor names the identity and wins",
			args:       []string{"--actor", seatIDSess, "update", id, "--claim"},
			assignee:   seatIDSess,
			actor:      seatNamePool,
			identities: seatIdentities(),
		},
		{
			name:       "inline --actor= names the identity and wins",
			args:       []string{"--actor=" + seatIDSess, "close", id},
			assignee:   seatIDSess,
			actor:      seatNamePool,
			identities: seatIdentities(),
		},
		{
			name:       "a batch is refused rather than partially substituted",
			args:       []string{"close", id, "cr-abc.2"},
			assignee:   seatIDSess,
			actor:      seatNamePool,
			identities: seatIdentities(),
		},
		{
			name:       "an unclassifiable flag fails closed",
			args:       []string{"update", id, "--claim", "--not-a-known-flag", "value"},
			assignee:   seatIDSess,
			actor:      seatNamePool,
			identities: seatIdentities(),
		},
		{
			// The claim verb of a verb we do not own must not be touched.
			name:       "delete is outside the identity gates",
			args:       []string{"delete", id},
			assignee:   seatIDSess,
			actor:      seatNamePool,
			identities: seatIdentities(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fetched := map[string]beads.Bead{}
			if tc.assignee != "" || strings.Contains(strings.Join(tc.args, " "), id) {
				fetched[id] = beads.Bead{ID: id, Assignee: tc.assignee}
			}
			got, ok := bdSeatIdentityActorOverride(tc.args, fetched, tc.identities, tc.actor)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("bdSeatIdentityActorOverride(%v, assignee=%q, actor=%q) = (%q, %v), want (%q, %v)",
					tc.args, tc.assignee, tc.actor, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestBDSeatIdentityRefusesUnreadBead pins that the substitution never rests on
// a bead the exact-ID guard could not read.
func TestBDSeatIdentityRefusesUnreadBead(t *testing.T) {
	if got, ok := bdSeatIdentityActorOverride([]string{"update", "cr-abc.1", "--claim"}, nil, seatIdentities(), seatNamePool); ok {
		t.Fatalf("override on an unread bead = (%q, true), want refusal; got %q", got, got)
	}
	fetched := map[string]beads.Bead{"cr-abc.9": {ID: "cr-abc.9", Assignee: seatIDSess}}
	if _, ok := bdSeatIdentityActorOverride([]string{"update", "cr-abc.1", "--claim"}, fetched, seatIdentities(), seatNamePool); ok {
		t.Fatal("override on a bead absent from the guard's reads must refuse")
	}
}

// TestBDSeatIdentityCandidatesFromEnv pins which environment forms a seat
// answers to — and that the bare pool template and the actor itself are not
// among them.
func TestBDSeatIdentityCandidatesFromEnv(t *testing.T) {
	env := []string{
		"GC_SESSION_ID=" + seatIDSess,
		"GC_SESSION_NAME=" + seatNamePool,
		"GC_ALIAS=nux",
		"GC_AGENT=nux",
		"GC_TEMPLATE=mechanic",
		"BEADS_ACTOR=" + seatNamePool,
		"PATH=/bin",
	}
	got := bdSeatIdentityCandidatesFromEnv(env)
	want := []string{seatIDSess, seatNamePool, "nux"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for _, banned := range []string{"mechanic", "BEADS_ACTOR"} {
		for _, c := range got {
			if c == banned {
				t.Fatalf("candidate set %v must not contain %q (ga-80pen8 / vacuous-actor)", got, banned)
			}
		}
	}
	if got := bdSeatIdentityCandidatesFromEnv([]string{"PATH=/bin"}); len(got) != 0 {
		t.Fatalf("candidates with no session context = %v, want empty", got)
	}
}

// TestBDSeatIdentityIdentitiesForSessionBead covers the two arms of the identity
// read: the alias_history form that only exists on the session bead, and the
// fail-closed fallback when that read is unavailable.
func TestBDSeatIdentityIdentitiesForSessionBead(t *testing.T) {
	store := beads.NewMemStore()
	sess := sessionFrontDoor(store)
	created, err := sess.CreateSessionInfo(sessionpkg.CreateSpec{
		Title:     seatNamePool,
		AgentName: "mechanic",
		Metadata: map[string]string{
			"session_name":  seatNamePool,
			"alias":         "nux",
			"alias_history": "old-nux",
		},
	})
	if err != nil {
		t.Fatalf("create session bead: %v", err)
	}
	if _, err := sess.Get(created.ID); err != nil {
		t.Fatalf("the created session bead is not readable by its own id %q: %v", created.ID, err)
	}
	env := []string{"GC_SESSION_ID=" + created.ID, "GC_SESSION_NAME=" + seatNamePool, "GC_AGENT=" + seatNamePool}

	identities := bdSeatIdentityIdentitiesFor(env, sess)
	for _, want := range []string{created.ID, seatNamePool, "nux", "old-nux"} {
		if !hookClaimHasIdentity(want, identities) {
			t.Fatalf("identities = %v, want %q among them (session-bead forms must be admitted)", identities, want)
		}
	}

	// A door that cannot answer (nil, or a session bead that does not exist)
	// leaves the environment set standing and never widens it.
	if got := bdSeatIdentityIdentitiesFor(env, nil); !reflect.DeepEqual(got, bdSeatIdentityCandidatesFromEnv(env)) {
		t.Fatalf("nil front door must fall back to the env set, got %v", got)
	}
	if got := bdSeatIdentityIdentitiesFor(env, sessionFrontDoor(beads.NewMemStore())); !reflect.DeepEqual(got, bdSeatIdentityCandidatesFromEnv(env)) {
		t.Fatalf("missing session bead must fall back to the env set, got %v", got)
	}

	// The alias_history form authorizes the seat's own verbs end to end: the
	// bead is assigned under a PRIOR alias, the actor arrives as the CURRENT
	// alias, and neither form is in the environment at all.
	const id = "cr-abc.1"
	actor := []string{"update", id, "--claim"}
	fetched := map[string]beads.Bead{id: {ID: id, Assignee: "old-nux"}}
	if sub, ok := bdSeatIdentityActorOverride(actor, fetched, identities, "nux"); !ok || sub != "old-nux" {
		t.Fatalf("alias_history arm: override = (%q, %v), want (\"old-nux\", true)", sub, ok)
	}
	// The same bead under a different seat's alias still refuses.
	if _, ok := bdSeatIdentityActorOverride(actor, fetched, identities, "other-nux"); ok {
		t.Fatal("an alias that is not this session's must not authorize the claim")
	}
}

// TestBDSeatIdentityWithActor pins the environment rewrite: one BEADS_ACTOR,
// set to the substituted identity, everything else carried verbatim.
func TestBDSeatIdentityWithActor(t *testing.T) {
	env := []string{"PATH=/bin", "BEADS_ACTOR=" + seatNamePool, "GC_SESSION_ID=" + seatIDSess}
	got := bdSeatIdentityWithActor(env, seatIDSess)
	actors := 0
	for _, entry := range got {
		if strings.HasPrefix(entry, "BEADS_ACTOR=") {
			actors++
			if entry != "BEADS_ACTOR="+seatIDSess {
				t.Fatalf("env = %v, want BEADS_ACTOR rewritten to the session id", got)
			}
		}
	}
	if actors != 1 {
		t.Fatalf("env = %v, want exactly one BEADS_ACTOR entry, got %d", got, actors)
	}
	if !reflect.DeepEqual(bdSeatIdentityWithActor([]string{"PATH=/bin"}, seatIDSess), []string{"PATH=/bin", "BEADS_ACTOR=" + seatIDSess}) {
		t.Fatal("a missing BEADS_ACTOR must be appended")
	}
}

// TestBDByIDClaimActorFor is the class-store door: a step bead already assigned
// to the session bead ID claims idempotently for a seat whose actor arrives as
// the pool label, and a bead held by a different session keeps the caller's own
// actor so the graph contract still reports a conflict.
func TestBDByIDClaimActorFor(t *testing.T) {
	t.Setenv("GC_SESSION_ID", seatIDSess)
	t.Setenv("GC_SESSION_NAME", seatNamePool)
	t.Setenv("GC_AGENT", seatNamePool)
	t.Setenv("BEADS_ACTOR", seatNamePool)
	t.Setenv("GC_TEMPLATE", "mechanic")

	if got := bdByIDClaimActorFor(seatIDSess); got != seatIDSess {
		t.Fatalf("claim of a session-id-assigned step bead = %q, want %q", got, seatIDSess)
	}
	if got := bdByIDClaimActorFor(seatNamePool); got != seatNamePool {
		t.Fatalf("same-form claim must keep its actor, got %q", got)
	}
	if got := bdByIDClaimActorFor(otherSessID); got != seatNamePool {
		t.Fatalf("another seat's assignment must keep the caller's actor so Claim conflicts, got %q", got)
	}
	if got := bdByIDClaimActorFor(""); got != seatNamePool {
		t.Fatalf("unassigned claim must keep the caller's actor, got %q", got)
	}

	// A process that is not a session at all (supervisor, order subprocess) is
	// left exactly as it is today.
	t.Setenv("GC_SESSION_ID", "")
	t.Setenv("GC_SESSION_NAME", "")
	t.Setenv("GC_AGENT", "")
	t.Setenv("BEADS_ACTOR", "order:machinery-drift")
	if got := bdByIDClaimActorFor(seatIDSess); got != "order:machinery-drift" {
		t.Fatalf("non-session actor must pass through, got %q", got)
	}
}
