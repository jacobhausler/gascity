package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/session/sessiontest"
)

// keyGapProvider is a grok-shaped resolved provider: gc can neither assign it
// a conversation id (no session_id_flag) nor be told one by a session-start
// hook (no hook surface), so its transcripts are unreachable by key.
func keyGapProvider() *config.ResolvedProvider {
	return &config.ResolvedProvider{Name: "grok", Command: "grok", BuiltinAncestor: "grok", ResumeFlag: "--resume"}
}

// assignedSessionCandidate builds a session bead with an in-progress work bead,
// the shape start preparation expects.
func assignedSessionCandidate(t *testing.T, store beads.Store, rp *config.ResolvedProvider) startCandidate {
	t.Helper()
	const sessionName = "worker"
	sessionBead, err := store.Create(beads.Bead{
		Title:    sessionName,
		Type:     sessionBeadType,
		Labels:   []string{sessionBeadLabel},
		Metadata: map[string]string{"session_name": sessionName, "template": "worker", "state": "asleep"},
	})
	if err != nil {
		t.Fatalf("Create(session): %v", err)
	}
	work, err := store.Create(beads.Bead{Title: "do the work", Type: "task", Assignee: sessionName})
	if err != nil {
		t.Fatalf("Create(work): %v", err)
	}
	status := "in_progress"
	if err := store.Update(work.ID, beads.UpdateOpts{Status: &status}); err != nil {
		t.Fatalf("mark work in_progress: %v", err)
	}
	return startCandidate{
		info: sessiontest.SeedBead(t, sessionBead),
		tp: TemplateParams{
			TemplateName:     "worker",
			SessionName:      sessionName,
			Command:          rp.Command,
			ResolvedProvider: rp,
		},
	}
}

func captureSessionIDContinuityWarn(t *testing.T) *bytes.Buffer {
	t.Helper()
	sessionIDContinuityWarned.Range(func(k, _ any) bool {
		sessionIDContinuityWarned.Delete(k)
		return true
	})
	buf := &bytes.Buffer{}
	previous := sessionIDContinuityWarnSink
	sessionIDContinuityWarnSink = buf
	t.Cleanup(func() { sessionIDContinuityWarnSink = previous })
	return buf
}

// The #6083 degradation must be stated at the moment gc declines to mint the
// key. Before this, the session started cleanly, looked healthy, and simply had
// no history — which is how the gap survived 94 sessions unnoticed.
func TestBuildPreparedStart_WarnsWhenProviderCannotEverHaveAConversationKey(t *testing.T) {
	store := beads.NewMemStore()
	buf := captureSessionIDContinuityWarn(t)

	_, info, err := buildPreparedStart(assignedSessionCandidate(t, store, keyGapProvider()), &config.City{}, store)
	if err != nil {
		t.Fatalf("buildPreparedStart: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"no route to a conversation id", "gc mints no session_key", "capability gap", "not a missing transcript"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning missing %q; got:\n%s", want, out)
		}
	}
	if strings.TrimSpace(info.SessionKey) != "" {
		t.Errorf("a provider gc cannot key must not gain a session_key, got %q", info.SessionKey)
	}
}

// The warning is once per session+provider per process: start preparation runs
// again on every wake and on every launch-only relaunch, and repeating a
// property of the provider each time is noise.
func TestSessionIDContinuityWarningIsDeduplicated(t *testing.T) {
	buf := captureSessionIDContinuityWarn(t)
	rp := keyGapProvider()
	warnSessionCannotAssignSessionID("worker", rp)
	warnSessionCannotAssignSessionID("worker", rp)
	if got := strings.Count(buf.String(), "no route to a conversation id"); got != 1 {
		t.Fatalf("warning count = %d, want 1:\n%s", got, buf.String())
	}
	warnSessionCannotAssignSessionID("other", rp)
	if got := strings.Count(buf.String(), "no route to a conversation id"); got != 2 {
		t.Fatalf("a different session must still be warned about, count = %d", got)
	}
}

// A provider that can be assigned an id must not hear about this, and must
// still get its minted key — the warning must never cry wolf on the lane that
// works today.
func TestBuildPreparedStart_DoesNotWarnWhenProviderCanAssignSessionID(t *testing.T) {
	store := beads.NewMemStore()
	buf := captureSessionIDContinuityWarn(t)
	rp := keyGapProvider()
	rp.SessionIDFlag = "--session-id"

	_, info, err := buildPreparedStart(assignedSessionCandidate(t, store, rp), &config.City{}, store)
	if err != nil {
		t.Fatalf("buildPreparedStart: %v", err)
	}
	if out := buf.String(); out != "" {
		t.Errorf("no warning expected for a key-assignable provider, got:\n%s", out)
	}
	if strings.TrimSpace(info.SessionKey) == "" {
		t.Errorf("session_key should still be minted for a provider with session_id_flag")
	}
	if b, err := store.Get(info.ID); err == nil && strings.TrimSpace(b.Metadata["session_key"]) == "" {
		t.Errorf("minted session_key was not persisted to the bead")
	}
}
