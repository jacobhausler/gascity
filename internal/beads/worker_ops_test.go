package beads_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// Every store a city can resolve as its WORK store must satisfy the one claim
// contract SQLiteStore.Claim states normatively, because ClaimFor dispatches to
// whichever store a city resolved and a caller must not be able to tell them
// apart. The table is the whole point: NativeDoltStore is the store a rig-backed
// city actually resolves, and while only the in-process stores were exercised
// here, /worker/claim answered 501 in production under a green suite.
func TestStoreClaimContract(t *testing.T) {
	for _, backend := range []struct {
		name     string
		newStore func(t *testing.T) beads.Store
	}{
		{"MemStore", func(t *testing.T) beads.Store { return beads.NewMemStore() }},
		{"NativeDoltStore", func(t *testing.T) beads.Store { return beads.NewNativeDoltStoreForConformance() }},
	} {
		t.Run(backend.name, func(t *testing.T) {
			newStore := func(t *testing.T) (beads.Store, beads.Bead) {
				t.Helper()
				s := backend.newStore(t)
				b, err := s.Create(beads.Bead{Title: "work"})
				if err != nil {
					t.Fatalf("create: %v", err)
				}
				return s, b
			}

			t.Run("unassigned open bead is claimed", func(t *testing.T) {
				s, b := newStore(t)
				claimed, ok, err := beads.ClaimFor(s, b.ID, "worker-1")
				if err != nil || !ok {
					t.Fatalf("claim: ok=%v err=%v", ok, err)
				}
				if claimed.Assignee != "worker-1" || claimed.Status != "in_progress" {
					t.Fatalf("claimed = %+v", claimed)
				}
			})

			t.Run("same holder reclaims idempotently", func(t *testing.T) {
				s, b := newStore(t)
				if _, ok, err := beads.ClaimFor(s, b.ID, "worker-1"); err != nil || !ok {
					t.Fatalf("first claim: ok=%v err=%v", ok, err)
				}
				before, _ := s.Get(b.ID)
				claimed, ok, err := beads.ClaimFor(s, b.ID, "worker-1")
				if err != nil || !ok {
					t.Fatalf("reclaim: ok=%v err=%v", ok, err)
				}
				if claimed.Revision != before.Revision {
					t.Fatalf("a same-owner reclaim consumed a revision: %d -> %d", before.Revision, claimed.Revision)
				}
			})

			t.Run("another holder is a conflict not an error", func(t *testing.T) {
				s, b := newStore(t)
				if _, ok, err := beads.ClaimFor(s, b.ID, "worker-1"); err != nil || !ok {
					t.Fatalf("first claim: ok=%v err=%v", ok, err)
				}
				claimed, ok, err := beads.ClaimFor(s, b.ID, "worker-2")
				if err != nil {
					t.Fatalf("conflict must not be an error: %v", err)
				}
				if ok || claimed.ID != "" {
					t.Fatalf("second claimant won: ok=%v bead=%+v", ok, claimed)
				}
			})

			t.Run("closed bead is not resurrected", func(t *testing.T) {
				s, b := newStore(t)
				if err := s.Close(b.ID); err != nil {
					t.Fatalf("close: %v", err)
				}
				if _, ok, err := beads.ClaimFor(s, b.ID, "worker-1"); ok || err != nil {
					t.Fatalf("claim of a closed bead: ok=%v err=%v", ok, err)
				}
			})

			t.Run("unknown bead is not found", func(t *testing.T) {
				s, _ := newStore(t)
				if _, _, err := beads.ClaimFor(s, "nope-1", "worker-1"); !errors.Is(err, beads.ErrNotFound) {
					t.Fatalf("err = %v, want ErrNotFound", err)
				}
			})

			t.Run("empty assignee is refused", func(t *testing.T) {
				s, b := newStore(t)
				if _, _, err := beads.ClaimFor(s, b.ID, "  "); err == nil {
					t.Fatal("an empty assignee must be refused, not claimed for nobody")
				}
			})

			t.Run("exactly one winner under concurrency", func(t *testing.T) {
				s, b := newStore(t)
				var wg sync.WaitGroup
				wins := make([]bool, 8)
				for i := range wins {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						_, ok, _ := beads.ClaimFor(s, b.ID, "worker-"+string(rune('a'+i)))
						wins[i] = ok
					}(i)
				}
				wg.Wait()
				count := 0
				for _, w := range wins {
					if w {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("%d winners, want exactly 1", count)
				}
			})
		})
	}
}

// The native Dolt store is the work store a rig-backed city resolves, so its
// comment capability is what decides whether /worker/comment answers 200 or
// 501 in production.
func TestNativeDoltStoreCommentContract(t *testing.T) {
	newStore := func(t *testing.T) (beads.Store, func(string) []string, beads.Bead) {
		t.Helper()
		s, log := beads.NewNativeDoltStoreWithCommentLog()
		b, err := s.Create(beads.Bead{Title: "work"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		return s, log, b
	}

	t.Run("comment is appended", func(t *testing.T) {
		s, log, b := newStore(t)
		if err := beads.CommentOn(s, b.ID, "claimed from alloc"); err != nil {
			t.Fatalf("CommentOn: %v", err)
		}
		got := log(b.ID)
		if len(got) != 1 || !strings.Contains(got[0], "claimed from alloc") {
			t.Fatalf("comment log = %v, want one entry carrying the text", got)
		}
	})

	t.Run("appends accumulate", func(t *testing.T) {
		s, log, b := newStore(t)
		for _, text := range []string{"one", "two"} {
			if err := beads.CommentOn(s, b.ID, text); err != nil {
				t.Fatalf("CommentOn %q: %v", text, err)
			}
		}
		if got := log(b.ID); len(got) != 2 {
			t.Fatalf("comment log = %v, want two entries", got)
		}
	})

	t.Run("empty text is refused", func(t *testing.T) {
		s, log, b := newStore(t)
		if err := beads.CommentOn(s, b.ID, "   "); !errors.Is(err, beads.ErrEmptyComment) {
			t.Fatalf("err = %v, want ErrEmptyComment", err)
		}
		if got := log(b.ID); len(got) != 0 {
			t.Fatalf("an empty comment was written: %v", got)
		}
	})

	t.Run("unknown bead is not found", func(t *testing.T) {
		s, log, _ := newStore(t)
		if err := beads.CommentOn(s, "nope-1", "note"); !errors.Is(err, beads.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if got := log("nope-1"); len(got) != 0 {
			t.Fatalf("a comment landed on a missing bead: %v", got)
		}
	})
}

// ClaimFor is the single discovery point for the two claim shapes, so a caller
// never repeats the type assertions and a store implementing neither fails
// with a named capability error rather than a panic.
func TestClaimForDispatchesAndNamesTheMissingCapability(t *testing.T) {
	s := beads.NewMemStore()
	b, err := s.Create(beads.Bead{Title: "work"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	claimed, ok, err := beads.ClaimFor(s, b.ID, "worker-1")
	if err != nil || !ok || claimed.Assignee != "worker-1" {
		t.Fatalf("ClaimFor on an assignee-claimer: ok=%v err=%v bead=%+v", ok, err, claimed)
	}

	if _, _, err := beads.ClaimFor(claimlessStore{s}, b.ID, "worker-1"); !errors.Is(err, beads.ErrClaimUnsupported) {
		t.Fatalf("err = %v, want ErrClaimUnsupported", err)
	}
	if _, _, err := beads.ClaimFor(nil, b.ID, "worker-1"); !errors.Is(err, beads.ErrClaimUnsupported) {
		t.Fatalf("nil store err = %v, want ErrClaimUnsupported", err)
	}
}

// claimlessStore hides MemStore's Claim behind a wrapper that forwards only the
// Store interface, standing in for a backend with no claim capability.
type claimlessStore struct{ beads.Store }

func TestCommentOnNamesTheMissingCapability(t *testing.T) {
	s := beads.NewMemStore()
	if err := beads.CommentOn(s, "mc-1", "note"); !errors.Is(err, beads.ErrCommentUnsupported) {
		t.Fatalf("err = %v, want ErrCommentUnsupported", err)
	}
	if err := beads.CommentOn(nil, "mc-1", "note"); !errors.Is(err, beads.ErrCommentUnsupported) {
		t.Fatalf("nil store err = %v, want ErrCommentUnsupported", err)
	}
}

// A claim made on someone else's behalf must name that actor explicitly: the
// store's process-wide BEADS_ACTOR belongs to the controller, not the session
// the bead is being claimed for.
func TestBdStoreClaimAsNamesTheActor(t *testing.T) {
	var got []string
	runner := func(_, name string, args ...string) ([]byte, error) {
		got = append([]string{name}, args...)
		return []byte(`{"id":"mc-1","title":"work","status":"in_progress","assignee":"worker-1"}`), nil
	}
	s := beads.NewBdStore("/city", runner)
	claimed, ok, err := s.ClaimAs("mc-1", "worker-1")
	if err != nil || !ok {
		t.Fatalf("ClaimAs: ok=%v err=%v", ok, err)
	}
	if claimed.Assignee != "worker-1" {
		t.Fatalf("claimed = %+v", claimed)
	}
	line := strings.Join(got, " ")
	if !strings.Contains(line, "--claim") || !strings.Contains(line, "--actor worker-1") {
		t.Fatalf("argv = %q, want the claim verb with an explicit actor", line)
	}

	// Claim (no assignee) must stay byte-identical to what it always sent.
	got = nil
	if _, _, err := s.Claim("mc-1"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if line := strings.Join(got, " "); strings.Contains(line, "--actor") {
		t.Fatalf("argv = %q, want no actor flag on the inherited-identity claim", line)
	}
}

// The comment verb is the same one `gc bd comment` reaches, and the text rides
// as a single argv element so a multi-word note survives verbatim.
func TestBdStoreCommentSendsTextAsOneArgument(t *testing.T) {
	var got []string
	runner := func(_, name string, args ...string) ([]byte, error) {
		got = append([]string{name}, args...)
		return []byte("{}"), nil
	}
	s := beads.NewBdStore("/city", runner)
	if err := s.Comment("mc-1", "two words"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if len(got) != 4 || got[1] != "comment" || got[2] != "mc-1" || got[3] != "two words" {
		t.Fatalf("argv = %q", got)
	}

	if err := s.Comment("mc-1", "   "); !errors.Is(err, beads.ErrEmptyComment) {
		t.Fatalf("err = %v, want ErrEmptyComment", err)
	}
}
