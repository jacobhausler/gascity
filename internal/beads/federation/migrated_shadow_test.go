package federation

import (
	"errors"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

type recordingStore struct {
	beads.Store
	queries []beads.ListQuery
	err     error
}

func (s *recordingStore) List(q beads.ListQuery) ([]beads.Bead, error) {
	s.queries = append(s.queries, q)
	if s.err != nil {
		return nil, s.err
	}
	return s.Store.List(q)
}

func TestMigratedWorkShadowIDsUsesCanonicalBindingAndOneBatch(t *testing.T) {
	graph := &recordingStore{Store: beads.NewMemStoreFrom(2, []beads.Bead{
		{ID: "work-1", Metadata: map[string]string{beadmeta.InfraMigratedFromMetadataKey: config.StorageWorkBinding}},
		{ID: "work-2", Metadata: map[string]string{beadmeta.InfraMigratedFromMetadataKey: "other"}},
	}, nil)}
	got, err := MigratedWorkShadowIDs(graph, []string{"work-1", "work-2"})
	if err != nil {
		t.Fatalf("MigratedWorkShadowIDs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("shadows=%v, want only canonical work marker", got)
	}
	if _, ok := got["work-1"]; !ok {
		t.Fatalf("shadows=%v, want work-1", got)
	}
	if len(graph.queries) != 1 {
		t.Fatalf("queries=%v, want one batch", graph.queries)
	}
	q := graph.queries[0]
	if len(q.IDs) != 2 || !q.IncludeClosed || q.TierMode != beads.FederatedReadTier {
		t.Fatalf("query=%+v, want both ids, IncludeClosed, FederatedReadTier", q)
	}
}

func TestMigratedWorkShadowIDsFailsClosed(t *testing.T) {
	graph := &recordingStore{Store: beads.NewMemStore(), err: errors.New("graph unavailable")}
	if _, err := MigratedWorkShadowIDs(graph, []string{"work-1"}); err == nil {
		t.Fatal("MigratedWorkShadowIDs returned nil error on graph failure")
	}
}
