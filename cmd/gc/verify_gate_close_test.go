package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/convoy"
)

// newCloseGateFixture builds the minimal close-gate population: an open
// workflow root that consumed convoy C, a convoy C that tracks target T, and
// an open close-gate step under the root. All IDs are explicit.
func newCloseGateFixture(t *testing.T) (store *beads.MemStore, rootID, convoyID, gateID, targetID string) {
	t.Helper()
	store = beads.NewMemStore()
	store.HonorExplicitIDs = true
	rootID, convoyID, gateID, targetID = "gcg--root", "cr-convoy", "gcg--gate", "cr-target"
	mk := func(id, typ string, meta map[string]string) {
		t.Helper()
		if _, err := store.Create(beads.Bead{ID: id, Title: id, Type: typ, Metadata: meta}); err != nil {
			t.Fatalf("creating %s: %v", id, err)
		}
	}
	mk(rootID, "task", map[string]string{
		beadmeta.KindMetadataKey:          beadmeta.KindWorkflow,
		beadmeta.InputConvoyIDMetadataKey: convoyID,
	})
	mk(convoyID, "convoy", nil)
	mk(gateID, "task", map[string]string{
		beadmeta.RootBeadIDMetadataKey: rootID,
		beadmeta.CloseGateMetadataKey:  verifyGateMarkerValue,
	})
	mk(targetID, "task", nil)
	if err := store.DepAdd(convoyID, targetID, convoy.TrackingDepType); err != nil {
		t.Fatalf("adding tracks dep: %v", err)
	}
	return store, rootID, convoyID, gateID, targetID
}

func TestVerifyGateCloseViolation(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(t *testing.T, store *beads.MemStore, rootID, convoyID, gateID, targetID string)
		callerID   string
		wantViol   bool
		wantSubstr string // substring expected in the violation; "" ⇒ none required
	}{
		{
			name:       "open gate step blocks a non-gate caller",
			callerID:   "gcg--other",
			wantViol:   true,
			wantSubstr: "refusing to close cr-target: open verification gate step gcg--gate (workflow gcg--root)",
		},
		{
			name:       "open gate step with unresolvable caller fails closed",
			callerID:   "",
			wantViol:   true,
			wantSubstr: "open verification gate step gcg--gate",
		},
		{
			name:     "the gate step's own session may close the target",
			callerID: "gcg--gate",
			wantViol: false,
		},
		{
			name: "a closed gate step is the ran-to-completion proof",
			mutate: func(t *testing.T, store *beads.MemStore, _, _, gateID, _ string) {
				if err := store.Close(gateID); err != nil {
					t.Fatalf("closing gate: %v", err)
				}
			},
			callerID: "gcg--other",
			wantViol: false,
		},
		{
			name: "a step without the marker is not a gate",
			mutate: func(t *testing.T, store *beads.MemStore, _, _, gateID, _ string) {
				if err := store.Update(gateID, beads.UpdateOpts{Metadata: map[string]string{beadmeta.CloseGateMetadataKey: ""}}); err != nil {
					t.Fatalf("clearing marker: %v", err)
				}
			},
			callerID: "gcg--other",
			wantViol: false,
		},
		{
			name: "a closed workflow root owes no further gate",
			mutate: func(t *testing.T, store *beads.MemStore, rootID, _, _, _ string) {
				if err := store.Close(rootID); err != nil {
					t.Fatalf("closing root: %v", err)
				}
			},
			callerID: "gcg--other",
			wantViol: false,
		},
		{
			name: "no tracks edge from the convoy to the target",
			mutate: func(t *testing.T, store *beads.MemStore, _, convoyID, _, targetID string) {
				if err := store.DepRemove(convoyID, targetID); err != nil {
					t.Fatalf("removing dep: %v", err)
				}
			},
			callerID: "gcg--other",
			wantViol: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, rootID, convoyID, gateID, targetID := newCloseGateFixture(t)
			if tc.mutate != nil {
				tc.mutate(t, store, rootID, convoyID, gateID, targetID)
			}
			violation := verifyGateCloseViolation(store, targetID, tc.callerID)
			if tc.wantViol && violation == "" {
				t.Fatalf("expected a violation, got none")
			}
			if !tc.wantViol && violation != "" {
				t.Fatalf("expected no violation, got %q", violation)
			}
			if tc.wantSubstr != "" && !strings.Contains(violation, tc.wantSubstr) {
				t.Fatalf("violation %q does not contain %q", violation, tc.wantSubstr)
			}
		})
	}
}

func TestVerifyGateCloseViolationIgnoresNonTracksEdges(t *testing.T) {
	store, _, _, gateID, targetID := newCloseGateFixture(t)
	// A "blocks" edge from an unrelated convoy to the target must not gate.
	if _, err := store.Create(beads.Bead{ID: "cr-other", Title: "cr-other", Type: "convoy"}); err != nil {
		t.Fatalf("creating convoy: %v", err)
	}
	if err := store.DepAdd("cr-other", targetID, "blocks"); err != nil {
		t.Fatalf("adding blocks dep: %v", err)
	}
	// And the fixture gate must not match a step under a different root.
	if _, err := store.Create(beads.Bead{
		ID:    "gcg--otherroot",
		Title: "gcg--otherroot",
		Type:  "task",
		Metadata: map[string]string{
			beadmeta.KindMetadataKey:          beadmeta.KindWorkflow,
			beadmeta.InputConvoyIDMetadataKey: "cr-other",
		},
	}); err != nil {
		t.Fatalf("creating other root: %v", err)
	}
	if _, err := store.Create(beads.Bead{
		ID:    "gcg--othergate",
		Title: "gcg--othergate",
		Type:  "task",
		Metadata: map[string]string{
			beadmeta.RootBeadIDMetadataKey: "gcg--otherroot",
			beadmeta.CloseGateMetadataKey:  verifyGateMarkerValue,
		},
	}); err != nil {
		t.Fatalf("creating other gate: %v", err)
	}
	if err := store.Close(gateID); err != nil {
		t.Fatalf("closing fixture gate: %v", err)
	}
	// The "blocks" edge is ignored (only "tracks" gates), and the fixture's
	// gate — the only close-gate step under a root consuming a tracks convoy —
	// is closed, so nothing may fire.
	if v := verifyGateCloseViolation(store, targetID, "gcg--nobody"); v != "" {
		t.Fatalf("expected no violation, got %q", v)
	}
}

func TestVerifyGateEnforceEnabled(t *testing.T) {
	tests := []struct {
		name  string
		value string
		set   bool
		want  bool
	}{
		{name: "unset enforces (inverted default)", want: true},
		{name: "empty enforces", value: "", set: true, want: true},
		{name: "off token downgrades to warn-only", value: "off", set: true, want: false},
		{name: "0 downgrades to warn-only", value: "0", set: true, want: false},
		{name: "false downgrades to warn-only", value: "false", set: true, want: false},
		{name: "no downgrades to warn-only", value: "no", set: true, want: false},
		{name: "1 enforces", value: "1", set: true, want: true},
		{name: "garbage enforces (only explicit off tokens downgrade)", value: "banana", set: true, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(verifyGateEnforceEnvVar, tc.value)
			} else {
				t.Setenv(verifyGateEnforceEnvVar, "")
				// t.Setenv cannot unset; the unset case is covered by the
				// empty-string case because the switch treats both alike.
			}
			if got := verifyGateEnforceEnabled(); got != tc.want {
				t.Fatalf("verifyGateEnforceEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunVerifyGateCloseGuard(t *testing.T) {
	closeArgs := func(targetID string) []string {
		return []string{"update", targetID, "--status=closed"}
	}
	t.Run("non-close invocation is out of the way", func(t *testing.T) {
		store, _, _, _, targetID := newCloseGateFixture(t)
		var stderr bytes.Buffer
		if got := runVerifyGateCloseGuard([]string{"show", targetID}, store, &stderr); got {
			t.Fatalf("expected no block for a read, got block")
		}
		if stderr.Len() != 0 {
			t.Fatalf("expected no output, got %q", stderr.String())
		}
	})
	t.Run("nil store fails open", func(t *testing.T) {
		_, _, _, _, targetID := newCloseGateFixture(t)
		var stderr bytes.Buffer
		if got := runVerifyGateCloseGuard(closeArgs(targetID), nil, &stderr); got {
			t.Fatalf("expected fail-open on nil store, got block")
		}
	})
	t.Run("open gate blocks a non-gate caller in enforce mode", func(t *testing.T) {
		store, _, _, _, targetID := newCloseGateFixture(t)
		t.Setenv("GC_BEAD_ID", "gcg--other")
		t.Setenv("GC_TRIGGER_BEAD_ID", "")
		t.Setenv("GC_SESSION_ID", "")
		var stderr bytes.Buffer
		if got := runVerifyGateCloseGuard(closeArgs(targetID), store, &stderr); !got {
			t.Fatalf("expected a block, got none")
		}
		if !strings.Contains(stderr.String(), "close-gate") || !strings.Contains(stderr.String(), "gcg--gate") {
			t.Fatalf("stderr %q missing gate message", stderr.String())
		}
	})
	t.Run("the gate step's own session is allowed", func(t *testing.T) {
		store, _, _, gateID, targetID := newCloseGateFixture(t)
		t.Setenv("GC_BEAD_ID", gateID)
		t.Setenv("GC_TRIGGER_BEAD_ID", "")
		t.Setenv("GC_SESSION_ID", "")
		var stderr bytes.Buffer
		if got := runVerifyGateCloseGuard(closeArgs(targetID), store, &stderr); got {
			t.Fatalf("expected the gate's own close to be allowed, got block")
		}
	})
	t.Run("warn-only mode logs but does not block", func(t *testing.T) {
		store, _, _, _, targetID := newCloseGateFixture(t)
		t.Setenv(verifyGateEnforceEnvVar, "off")
		t.Setenv("GC_BEAD_ID", "gcg--other")
		t.Setenv("GC_TRIGGER_BEAD_ID", "")
		t.Setenv("GC_SESSION_ID", "")
		var stderr bytes.Buffer
		if got := runVerifyGateCloseGuard(closeArgs(targetID), store, &stderr); got {
			t.Fatalf("expected warn-only to not block, got block")
		}
		if !strings.Contains(stderr.String(), "warn-only") {
			t.Fatalf("stderr %q missing warn-only notice", stderr.String())
		}
	})
}
