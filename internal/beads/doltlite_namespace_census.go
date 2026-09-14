//go:build gascity_native_beads

package beads

import (
	"database/sql"
	"errors"
	"fmt"
)

var _ NamespaceCensus = (*DoltliteReadStore)(nil)

// HasResidentOutside reports whether an open bead's id falls outside all
// prefixes in either DoltLite tier. It selects only id/status and stops at the
// first match, so the boot census does not decode the full bead history.
func (s *DoltliteReadStore) HasResidentOutside(prefixes []string) (bool, error) {
	where, args := namespaceExclusionSQL("COALESCE(i.id, '')", prefixes)
	for _, tables := range doltliteTableSetsForMode(FederatedReadTier) {
		if tables.wisps && !s.tableExists(tables.issues) {
			continue
		}
		query := "SELECT 1 FROM " + tables.issues + " i WHERE i.status <> 'closed'"
		if where != "" {
			query += " AND " + where
		}
		query += " LIMIT 1"

		var found int
		switch err := s.db.QueryRow(query, args...).Scan(&found); {
		case errors.Is(err, sql.ErrNoRows):
			continue
		case err != nil:
			return false, fmt.Errorf("censusing the id namespaces of doltlite table %s: %w", tables.issues, err)
		default:
			return true, nil
		}
	}
	return false, nil
}
