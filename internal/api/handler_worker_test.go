package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/session"
)

// workerFixture is one city with a session bead and one routed work bead,
// which is all any worker operation needs.
type workerFixture struct {
	srv     *Server
	store   *workerAtomicStoreDraft
	session beads.Bead
	work    beads.Bead
}

func newWorkerFixture(t *testing.T) *workerFixture {
	t.Helper()
	st := newFakeState(t)
	store := &workerAtomicStoreDraft{Store: beads.NewAtomicCloseMemStore()}
	st.cityBeadStore = store
	st.stores = map[string]beads.Store{"myrig": store}

	sess, err := store.Create(beads.Bead{
		Title:  "session",
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
	})
	if err != nil {
		t.Fatalf("create session bead: %v", err)
	}
	work, err := store.Create(beads.Bead{Title: "routed work", Assignee: ""})
	if err != nil {
		t.Fatalf("create work bead: %v", err)
	}
	return &workerFixture{srv: New(st), store: store, session: sess, work: work}
}

func (f *workerFixture) claim(t *testing.T) (*WorkerClaimOutput, error) {
	t.Helper()
	in := &WorkerClaimInput{}
	in.Body.SessionID = f.session.ID
	in.Body.Assignee = "worker-1"
	in.Body.BeadID = f.work.ID
	return f.srv.humaHandleWorkerClaim(context.Background(), in)
}

func problemStatus(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var se interface{ GetStatus() int }
	if errors.As(err, &se) {
		return se.GetStatus()
	}
	t.Fatalf("error %v is not a status-bearing problem", err)
	return 0
}

// A claim delegates to the store's CAS and stamps the session pointer, so a
// later `worker/current` can name the bead the session is executing.
func TestWorkerClaimWinsAndStampsSessionPointer(t *testing.T) {
	f := newWorkerFixture(t)

	out, err := f.claim(t)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if out.Body.Status != "claimed" || out.Body.Bead.ID != f.work.ID {
		t.Fatalf("claim returned %+v", out.Body)
	}
	if out.Body.Bead.Assignee != "worker-1" || out.Body.Bead.Status != "in_progress" {
		t.Fatalf("claimed bead not held: %+v", out.Body.Bead)
	}

	cur := &WorkerCurrentInput{SessionID: f.session.ID}
	got, err := f.srv.humaHandleWorkerCurrent(context.Background(), cur)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if got.Body.BeadID != f.work.ID {
		t.Fatalf("current = %q, want %q", got.Body.BeadID, f.work.ID)
	}
}

// The route must not weaken single-winner claiming: the second claimant loses
// with a 409 and the winner keeps the bead.
func TestWorkerClaimSecondAssigneeConflicts(t *testing.T) {
	f := newWorkerFixture(t)
	if _, err := f.claim(t); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	// A second session, so the session-pointer reservation is not what refuses.
	other, err := f.store.Create(beads.Bead{
		Title:  "session-2",
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
	})
	if err != nil {
		t.Fatalf("create second session: %v", err)
	}
	in := &WorkerClaimInput{}
	in.Body.SessionID = other.ID
	in.Body.Assignee = "worker-2"
	in.Body.BeadID = f.work.ID
	_, err = f.srv.humaHandleWorkerClaim(context.Background(), in)
	if got := problemStatus(t, err); got != 409 {
		t.Fatalf("second claim status = %d, want 409", got)
	}

	held, err := f.store.Get(f.work.ID)
	if err != nil {
		t.Fatalf("get work bead: %v", err)
	}
	if held.Assignee != "worker-1" {
		t.Fatalf("winner lost the bead: assignee = %q", held.Assignee)
	}
}

// A lost claim must not leave the session pointing at a bead it does not hold.
func TestWorkerClaimUnwindsSessionPointerOnConflict(t *testing.T) {
	f := newWorkerFixture(t)
	// Someone else already holds it.
	if _, ok, err := f.store.Claim(f.work.ID, "someone-else"); err != nil || !ok {
		t.Fatalf("seed claim: ok=%v err=%v", ok, err)
	}

	if _, err := f.claim(t); problemStatus(t, err) != 409 {
		t.Fatalf("claim should conflict, got %v", err)
	}
	sess, err := f.store.Get(f.session.ID)
	if err != nil {
		t.Fatalf("get session bead: %v", err)
	}
	if got := strings.TrimSpace(sess.Metadata[beadmeta.CurrentClaimBeadIDMetadataKey]); got != "" {
		t.Fatalf("session pointer left at %q after a lost claim", got)
	}
}

// A pointer left naming a bead that has since closed is a leftover, not a held
// claim. It must not wedge the session out of ever claiming again — which is
// exactly what a CAS hardcoded to expect "" did, on the ordinary end state of
// every completed remote claim.
func TestWorkerClaimStalePointerToClosedBeadDoesNotWedge(t *testing.T) {
	f := newWorkerFixture(t)
	if _, err := f.claim(t); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := f.store.Close(f.work.ID); err != nil {
		t.Fatalf("close claimed bead: %v", err)
	}

	next, err := f.store.Create(beads.Bead{Title: "next routed work"})
	if err != nil {
		t.Fatalf("create next work bead: %v", err)
	}
	in := &WorkerClaimInput{}
	in.Body.SessionID = f.session.ID
	in.Body.Assignee = "worker-1"
	in.Body.BeadID = next.ID
	out, err := f.srv.humaHandleWorkerClaim(context.Background(), in)
	if err != nil {
		t.Fatalf("claim after a stale pointer: %v", err)
	}
	if out.Body.Bead.ID != next.ID {
		t.Fatalf("claimed %q, want %q", out.Body.Bead.ID, next.ID)
	}
	sess, err := f.store.Get(f.session.ID)
	if err != nil {
		t.Fatalf("get session bead: %v", err)
	}
	if got := strings.TrimSpace(sess.Metadata[beadmeta.CurrentClaimBeadIDMetadataKey]); got != next.ID {
		t.Fatalf("session pointer = %q, want %q", got, next.ID)
	}
}

// A pointer naming work the session is still executing is a real held claim,
// and a second claim under it is refused: one session cannot hold two claims it
// can only report one of.
func TestWorkerClaimLivePointerStillConflicts(t *testing.T) {
	f := newWorkerFixture(t)
	if _, err := f.claim(t); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	next, err := f.store.Create(beads.Bead{Title: "second routed work"})
	if err != nil {
		t.Fatalf("create next work bead: %v", err)
	}
	in := &WorkerClaimInput{}
	in.Body.SessionID = f.session.ID
	in.Body.Assignee = "worker-1"
	in.Body.BeadID = next.ID
	if got := problemStatus(t, mustClaimErr(t, f, in)); got != 409 {
		t.Fatalf("second claim status = %d, want 409", got)
	}
	sess, err := f.store.Get(f.session.ID)
	if err != nil {
		t.Fatalf("get session bead: %v", err)
	}
	if got := strings.TrimSpace(sess.Metadata[beadmeta.CurrentClaimBeadIDMetadataKey]); got != f.work.ID {
		t.Fatalf("session pointer moved to %q, want the held bead %q", got, f.work.ID)
	}
	held, err := f.store.Get(next.ID)
	if err != nil {
		t.Fatalf("get second work bead: %v", err)
	}
	if strings.TrimSpace(held.Assignee) != "" {
		t.Fatalf("refused claim still took the bead: assignee = %q", held.Assignee)
	}
}

// A caller that decided to claim while looking at a pointer that has since moved
// is acting on a stale snapshot, and is refused rather than allowed to overwrite
// whatever replaced it.
func TestWorkerClaimObservedPointerFencesTheCAS(t *testing.T) {
	f := newWorkerFixture(t)
	if _, err := f.claim(t); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := f.store.Close(f.work.ID); err != nil {
		t.Fatalf("close claimed bead: %v", err)
	}

	next, err := f.store.Create(beads.Bead{Title: "next routed work"})
	if err != nil {
		t.Fatalf("create next work bead: %v", err)
	}
	in := &WorkerClaimInput{}
	in.Body.SessionID = f.session.ID
	in.Body.Assignee = "worker-1"
	in.Body.BeadID = next.ID
	in.Body.CurrentClaim = "some-other-bead"
	if got := problemStatus(t, mustClaimErr(t, f, in)); got != 409 {
		t.Fatalf("claim on a moved snapshot status = %d, want 409", got)
	}

	// The same request with the pointer the caller actually would have read wins.
	in.Body.CurrentClaim = f.work.ID
	if _, err := f.srv.humaHandleWorkerClaim(context.Background(), in); err != nil {
		t.Fatalf("claim with the observed pointer: %v", err)
	}
}

// A pointer naming a bead the dead-assignee lane handed to somebody else is a
// leftover too, even though that bead is very much in_progress. Reading only the
// status would wedge the session behind work it demonstrably does not hold.
func TestWorkerClaimPointerReclaimedByAnotherAssigneeDoesNotWedge(t *testing.T) {
	f := newWorkerFixture(t)
	if _, err := f.claim(t); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// The lane gives the bead to a live worker while the pointer still names it.
	if err := f.store.Update(f.work.ID, beads.UpdateOpts{Assignee: strPtr("worker-2")}); err != nil {
		t.Fatalf("reassign work bead: %v", err)
	}

	next, err := f.store.Create(beads.Bead{Title: "next routed work"})
	if err != nil {
		t.Fatalf("create next work bead: %v", err)
	}
	in := &WorkerClaimInput{}
	in.Body.SessionID = f.session.ID
	in.Body.Assignee = "worker-1"
	in.Body.BeadID = next.ID
	if _, err := f.srv.humaHandleWorkerClaim(context.Background(), in); err != nil {
		t.Fatalf("claim after the pointer target changed hands: %v", err)
	}
}

func strPtr(s string) *string { return &s }

func mustClaimErr(t *testing.T, f *workerFixture, in *WorkerClaimInput) error {
	t.Helper()
	out, err := f.srv.humaHandleWorkerClaim(context.Background(), in)
	if err == nil {
		t.Fatalf("claim unexpectedly succeeded: %+v", out.Body)
	}
	return err
}

// Release is the CAS dual: it applies while the holder still matches, and
// reports "skipped" — not an error — once the snapshot has moved.
func TestWorkerReleaseAppliesThenSkips(t *testing.T) {
	f := newWorkerFixture(t)
	if _, err := f.claim(t); err != nil {
		t.Fatalf("claim: %v", err)
	}

	in := &WorkerReleaseInput{}
	in.Body.SessionID = f.session.ID
	in.Body.Assignee = "worker-1"
	in.Body.BeadID = f.work.ID
	out, err := f.srv.humaHandleWorkerRelease(context.Background(), in)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if out.Body.Status != "released" {
		t.Fatalf("release status = %q, want released", out.Body.Status)
	}

	out, err = f.srv.humaHandleWorkerRelease(context.Background(), in)
	if err != nil {
		t.Fatalf("second release: %v", err)
	}
	if out.Body.Status != "skipped" {
		t.Fatalf("second release status = %q, want skipped", out.Body.Status)
	}
}

// A session that has claimed nothing is a fact, not a 404: the caller's own
// exit contract decides whether an empty answer is a failure.
func TestWorkerCurrentEmptyForUnclaimedSession(t *testing.T) {
	f := newWorkerFixture(t)
	out, err := f.srv.humaHandleWorkerCurrent(context.Background(), &WorkerCurrentInput{SessionID: f.session.ID})
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if out.Body.BeadID != "" {
		t.Fatalf("current = %q, want empty", out.Body.BeadID)
	}
}

func TestWorkerCurrentUnknownSessionIs404(t *testing.T) {
	f := newWorkerFixture(t)
	_, err := f.srv.humaHandleWorkerCurrent(context.Background(), &WorkerCurrentInput{SessionID: "nope-1"})
	if got := problemStatus(t, err); got != 404 {
		t.Fatalf("status = %d, want 404", got)
	}
}

// Drain-ack gives back the claim the session still holds, sets the metadata
// the reconciler waits on, and pokes the controller.
func TestWorkerDrainAckReleasesAndAcknowledges(t *testing.T) {
	f := newWorkerFixture(t)
	st := f.srv.state.(*fakeState)
	if _, err := f.claim(t); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// The release identity comes off the session bead, so give it one.
	if err := f.store.SetMetadata(f.session.ID, "alias", "worker-1"); err != nil {
		t.Fatalf("set alias: %v", err)
	}

	in := &WorkerDrainAckInput{}
	in.Body.SessionID = f.session.ID
	out, err := f.srv.humaHandleWorkerDrainAck(context.Background(), in)
	if err != nil {
		t.Fatalf("drain-ack: %v", err)
	}
	if out.Body.Status != "acknowledged" || out.Body.Released != f.work.ID {
		t.Fatalf("drain-ack returned %+v", out.Body)
	}
	if out.Body.ReleaseStatus != "released" {
		t.Fatalf("release_status = %q, want released", out.Body.ReleaseStatus)
	}
	held, err := f.store.Get(f.work.ID)
	if err != nil {
		t.Fatalf("get work bead: %v", err)
	}
	if held.Assignee != "" || held.Status != "open" {
		t.Fatalf("bead not handed back: %+v", held)
	}
	if st.pokeCount == 0 {
		t.Fatal("controller was not poked after the acknowledgement")
	}
}

// A drain-ack whose release the CAS declined still acknowledges — the ack is
// what the controller is blocked on — but says "skipped", so the caller can tell
// a clean drain from one whose bead had already moved on.
func TestWorkerDrainAckReportsASkippedRelease(t *testing.T) {
	f := newWorkerFixture(t)
	if _, err := f.claim(t); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.store.SetMetadata(f.session.ID, "alias", "worker-1"); err != nil {
		t.Fatalf("set alias: %v", err)
	}
	// The bead changes hands before the drain lands: the pointer still names it,
	// but it is no longer ours to give back.
	if err := f.store.Update(f.work.ID, beads.UpdateOpts{Assignee: strPtr("worker-2")}); err != nil {
		t.Fatalf("reassign work bead: %v", err)
	}

	in := &WorkerDrainAckInput{}
	in.Body.SessionID = f.session.ID
	out, err := f.srv.humaHandleWorkerDrainAck(context.Background(), in)
	if err != nil {
		t.Fatalf("drain-ack: %v", err)
	}
	if out.Body.Status != "acknowledged" {
		t.Fatalf("status = %q, want acknowledged", out.Body.Status)
	}
	if out.Body.Released != f.work.ID || out.Body.ReleaseStatus != "skipped" {
		t.Fatalf("drain-ack returned %+v, want released=%s release_status=skipped", out.Body, f.work.ID)
	}
	held, err := f.store.Get(f.work.ID)
	if err != nil {
		t.Fatalf("get work bead: %v", err)
	}
	if held.Assignee != "worker-2" {
		t.Fatalf("a skipped release still took the bead from its holder: %+v", held)
	}
}

// A session that never held anything acknowledges with no release outcome at
// all — "" is distinct from "skipped", which reports an attempt that lost.
func TestWorkerDrainAckWithNothingHeldReportsNoRelease(t *testing.T) {
	f := newWorkerFixture(t)
	in := &WorkerDrainAckInput{}
	in.Body.SessionID = f.session.ID
	out, err := f.srv.humaHandleWorkerDrainAck(context.Background(), in)
	if err != nil {
		t.Fatalf("drain-ack: %v", err)
	}
	if out.Body.Released != "" || out.Body.ReleaseStatus != "" {
		t.Fatalf("drain-ack returned %+v, want both release fields empty", out.Body)
	}
}

// The drain-ack release identity must be the identity the claim was written
// under. remoteWorkerIdentities ends at the session id, so this ladder must too:
// a session with no alias, no agent name, and no provider session name claims
// under its id, and a ladder that stopped one rung short released against "" and
// skipped every time.
func TestWorkerDrainAckAssigneeLadderEndsAtTheSessionID(t *testing.T) {
	for _, tc := range []struct {
		name string
		info session.Info
		want string
	}{
		{"alias wins", session.Info{Alias: "a", AgentName: "b", SessionName: "c", ID: "d"}, "a"},
		{"agent name next", session.Info{AgentName: "b", SessionName: "c", ID: "d"}, "b"},
		{"then the provider session name", session.Info{SessionName: "c", ID: "d"}, "c"},
		{"and finally the session id", session.Info{ID: "d"}, "d"},
		{"blanks do not count", session.Info{Alias: "  ", AgentName: " ", ID: "d"}, "d"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := workerDrainAckAssignee(tc.info); got != tc.want {
				t.Fatalf("workerDrainAckAssignee = %q, want %q", got, tc.want)
			}
		})
	}
}

// The claiming ladder is the other half of the same contract, and the two are
// only correct together.
func TestWorkerDrainAckLadderMatchesTheClaimingLadder(t *testing.T) {
	info := session.Info{Alias: "alias-1", AgentName: "agent-1", SessionName: "sess-1", ID: "bead-1"}
	rungs := []session.Info{
		info,
		{AgentName: info.AgentName, SessionName: info.SessionName, ID: info.ID},
		{SessionName: info.SessionName, ID: info.ID},
		{ID: info.ID},
	}
	want := []string{info.Alias, info.AgentName, info.SessionName, info.ID}
	for i, rung := range rungs {
		if got := workerDrainAckAssignee(rung); got != want[i] {
			t.Fatalf("rung %d = %q, want %q", i, got, want[i])
		}
	}
}

func TestWorkerCloseClosesTheBead(t *testing.T) {
	f := newWorkerFixture(t)
	if _, err := f.claim(t); err != nil {
		t.Fatalf("claim: %v", err)
	}
	in := &WorkerCloseInput{}
	in.Body.SessionID = f.session.ID
	in.Body.Assignee = "worker-1"
	in.Body.BeadID = f.work.ID
	in.Body.Outcome = beadmeta.WorkOutcomeNoOp
	in.Body.Reason = "test close"
	if _, err := f.srv.humaHandleWorkerClose(context.Background(), in); err != nil {
		t.Fatalf("close: %v", err)
	}
	got, err := f.store.Get(f.work.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "closed" {
		t.Fatalf("status = %q, want closed", got.Status)
	}
}

// commentingStore is a MemStore that also keeps a comment log, standing in for
// the ledger-backed stores that implement beads.Commenter in production.
type commentingStore struct {
	*beads.MemStore
	comments map[string][]string
}

func (c *commentingStore) Comment(id, text string) error {
	if _, err := c.Get(id); err != nil {
		return err
	}
	if c.comments == nil {
		c.comments = map[string][]string{}
	}
	c.comments[id] = append(c.comments[id], text)
	return nil
}

func TestWorkerCommentAppends(t *testing.T) {
	st := newFakeState(t)
	store := &commentingStore{MemStore: beads.NewMemStore()}
	st.cityBeadStore = store
	st.stores = map[string]beads.Store{"myrig": store}
	srv := New(st)
	work, err := store.Create(beads.Bead{Title: "work"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	in := &WorkerCommentInput{}
	in.Body.BeadID = work.ID
	in.Body.Text = "progress note"
	if _, err := srv.humaHandleWorkerComment(context.Background(), in); err != nil {
		t.Fatalf("comment: %v", err)
	}
	if got := store.comments[work.ID]; len(got) != 1 || got[0] != "progress note" {
		t.Fatalf("comment log = %v", got)
	}
}

// A store with no comment log must say so rather than silently succeed.
func TestWorkerCommentUnsupportedStoreIs501(t *testing.T) {
	f := newWorkerFixture(t)
	in := &WorkerCommentInput{}
	in.Body.BeadID = f.work.ID
	in.Body.Text = "note"
	_, err := f.srv.humaHandleWorkerComment(context.Background(), in)
	if got := problemStatus(t, err); got != 501 {
		t.Fatalf("status = %d, want 501", got)
	}
}

func TestWorkerClaimRequiresEveryField(t *testing.T) {
	f := newWorkerFixture(t)
	in := &WorkerClaimInput{}
	in.Body.SessionID = f.session.ID
	in.Body.BeadID = f.work.ID
	_, err := f.srv.humaHandleWorkerClaim(context.Background(), in)
	if got := problemStatus(t, err); got != 400 {
		t.Fatalf("status = %d, want 400", got)
	}
	if !strings.Contains(err.Error(), "assignee") {
		t.Fatalf("error does not name the missing field: %v", err)
	}
}

// End to end over the registered routes: a worker claims a bead and closes it
// through the mux, exactly as an off-host session would.
func TestWorkerClaimAndCloseThroughTheAPI(t *testing.T) {
	f := newWorkerFixture(t)

	claimIn := &WorkerClaimInput{}
	claimIn.Body.SessionID = f.session.ID
	claimIn.Body.Assignee = "worker-1"
	claimIn.Body.BeadID = f.work.ID
	if _, err := f.srv.humaHandleWorkerClaim(context.Background(), claimIn); err != nil {
		t.Fatalf("claim: %v", err)
	}

	curOut, err := f.srv.humaHandleWorkerCurrent(context.Background(), &WorkerCurrentInput{SessionID: f.session.ID})
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	closeIn := &WorkerCloseInput{}
	closeIn.Body.SessionID = f.session.ID
	closeIn.Body.Assignee = "worker-1"
	closeIn.Body.BeadID = curOut.Body.BeadID
	closeIn.Body.Outcome = beadmeta.WorkOutcomeNoOp
	closeIn.Body.Reason = "test close"
	if _, err := f.srv.humaHandleWorkerClose(context.Background(), closeIn); err != nil {
		t.Fatalf("close: %v", err)
	}

	got, err := f.store.Get(f.work.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "closed" || got.Assignee != "worker-1" {
		t.Fatalf("final bead = %+v", got)
	}
}
