package main

import (
	"io"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// TestBuildDesiredStateDefaultPoolDemandFederatesRoutedWork is the regression
// proof for cr-msizt / gastownhall#6019. A city-scoped default pool must count
// the same routed-work legs that its claim reader serves, including the city
// work ledger when coordination classes are relocated to a binding.
func TestBuildDesiredStateDefaultPoolDemandFederatesRoutedWork(t *testing.T) {
	cityPath := t.TempDir()
	work := beads.NewMemStoreFrom(1, nil, nil)
	binding := beads.NewMemStoreFrom(1000, nil, nil)
	routes := splitRoutes(binding)
	registerResidencyRoutes(cityPath, routes, func() beads.Store { return work })
	t.Cleanup(func() { unregisterResidencyRoutes(cityPath, routes) })

	const target = "worker"
	for _, tc := range []struct {
		name  string
		store beads.Store
		id    string
	}{
		{name: "work ledger", store: work, id: "work-1"},
		{name: "class binding", store: binding, id: "binding-1"},
	} {
		if _, err := tc.store.Create(beads.Bead{
			ID:       tc.id,
			Title:    tc.name,
			Type:     "task",
			Status:   "open",
			Metadata: map[string]string{"gc.routed_to": target},
		}); err != nil {
			t.Fatalf("create %s bead: %v", tc.name, err)
		}
	}

	minSessions, maxSessions := 0, 3
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{{
			Name:              target,
			MinActiveSessions: &minSessions,
			MaxActiveSessions: &maxSessions,
			Provider:          "mock",
		}},
		Providers: map[string]config.ProviderSpec{"mock": {Command: "true"}},
	}

	result := buildDesiredStateWithSessionBeads(
		"test-city", cityPath, time.Now(), cfg, runtime.NewFake(), binding, nil,
		&sessionBeadSnapshot{}, nil, io.Discard,
	)
	if got := result.ScaleCheckCounts[target]; got != 2 {
		t.Fatalf("default pool demand = %d, want 2 for one routed bead in each claimable leg; counts=%v", got, result.ScaleCheckCounts)
	}
}
