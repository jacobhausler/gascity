// Package federation contains shared read-side rules for merging bead stores.
package federation

import (
	"fmt"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// MigratedWorkShadowIDs returns graph rows whose explicit migration marker says
// they were copied from the canonical work binding. The caller supplies all
// work-leg candidate ids so the authoritative graph read is one batch. Closed
// rows are included because a closed twin is still evidence that an open work
// copy is a retained migration shadow.
func MigratedWorkShadowIDs(graph beads.Store, workIDs []string) (map[string]struct{}, error) {
	if graph == nil || len(workIDs) == 0 {
		return nil, nil
	}
	rows, err := graph.List(beads.ListQuery{
		IDs:           workIDs,
		IncludeClosed: true,
		TierMode:      beads.FederatedReadTier,
	})
	if err != nil {
		return nil, fmt.Errorf("checking migrated work shadows: %w", err)
	}
	shadows := make(map[string]struct{})
	for _, row := range rows {
		if row.Metadata[beadmeta.InfraMigratedFromMetadataKey] == config.StorageWorkBinding {
			shadows[row.ID] = struct{}{}
		}
	}
	return shadows, nil
}
