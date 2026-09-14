package beads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// NamespaceCensus is an optional Store capability for the boot-time residency
// verdict. It answers whether any resident bead falls outside every declared
// namespace without hydrating the bead rows.
//
// This is deliberately a capability rather than a ListQuery field: callers
// need a yes/no verdict, and a backend that cannot answer the exact question
// must be discovered as incapable so the caller can use its sound scan
// fallback. Returning false for an unsupported store could retire a by-ID
// probe over a resident relic.
type NamespaceCensus interface {
	HasResidentOutside(prefixes []string) (bool, error)
}

// ErrNamespaceCensusUnsupported reports that a store cannot answer the
// namespace-residency verdict directly.
var ErrNamespaceCensusUnsupported = errors.New("namespace census unsupported")

// NamespaceCensusHandleProvider lets a wrapper expose the capability of its
// backing store without claiming that every backing can answer it. This is
// needed by decorators that are required to carry the concrete backend's
// method set for other optional capabilities.
type NamespaceCensusHandleProvider interface {
	NamespaceCensusHandle() (NamespaceCensus, bool)
}

// NamespaceCensusFor discovers a real namespace census capability. Providers
// are consulted before a direct assertion because a structural wrapper may
// carry HasResidentOutside while its backing cannot answer the question.
func NamespaceCensusFor(store Store) (NamespaceCensus, bool) {
	if store == nil {
		return nil, false
	}
	if provider, ok := store.(NamespaceCensusHandleProvider); ok {
		return provider.NamespaceCensusHandle()
	}
	census, ok := store.(NamespaceCensus)
	return census, ok
}

// namespaceExclusionSQL returns the exact configured-prefix predicate used by
// storeref.IDInNamespace. LIKE is intentionally not used: it would treat '%'
// and '_' in a prefix as wildcards and would also swallow a neighboring
// namespace such as gcgx when the configured prefix is gcg.
func namespaceExclusionSQL(idExpr string, prefixes []string) (string, []any) {
	var clauses []string
	var args []any
	for _, prefix := range prefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			continue
		}
		clauses = append(clauses, "("+idExpr+" = ? OR substr("+idExpr+", 1, length(?)) = ?)")
		args = append(args, prefix, prefix+"-", prefix+"-")
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return "NOT (" + strings.Join(clauses, " OR ") + ")", args
}

var _ NamespaceCensus = (*SQLiteStore)(nil)

// HasResidentOutside reports whether an open bead's id falls outside all
// prefixes. It reads only the indexed id/status columns and stops at the first
// match; no bead_json is selected or decoded.
func (s *SQLiteStore) HasResidentOutside(prefixes []string) (bool, error) {
	if err := s.ensureOpen(); err != nil {
		return false, err
	}
	query := "SELECT 1 FROM beads b WHERE b.status <> 'closed'"
	where, args := namespaceExclusionSQL("COALESCE(b.id, '')", prefixes)
	if where != "" {
		query += " AND " + where
	}
	query += " LIMIT 1"

	var found int
	switch err := s.readDB.QueryRowContext(context.Background(), query, args...).Scan(&found); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("censusing the id namespaces of the sqlite bead store at %s: %w", s.path, err)
	default:
		return true, nil
	}
}
