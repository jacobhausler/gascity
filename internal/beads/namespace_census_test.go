package beads

import "testing"

func openNamespaceCensusStore(t *testing.T) *SQLiteStore {
	t.Helper()
	opened, err := OpenSQLiteStore(t.TempDir(), WithSQLiteStoreIDPrefix("gcg"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	store, ok := opened.(*SQLiteStore)
	if !ok {
		t.Fatalf("OpenSQLiteStore returned %T, want *SQLiteStore", opened)
	}
	t.Cleanup(func() { _ = store.CloseStore() })
	return store
}

func TestSQLiteHasResidentOutside(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ids    []string
		closed string
		want   bool
	}{
		{name: "foreign open bead", ids: []string{"gcg-1", "ga-relic"}, want: true},
		{name: "only claimed namespaces", ids: []string{"gcg-1", "gcnq-2"}, want: false},
		{name: "closed foreign bead is not open resident", ids: []string{"ga-done"}, closed: "ga-done", want: false},
		{name: "lookalike prefix is foreign", ids: []string{"gcgx-1"}, want: true},
		{name: "bare prefix is claimed", ids: []string{"gcg"}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openNamespaceCensusStore(t)
			for _, id := range tc.ids {
				if _, err := store.Create(Bead{ID: id, Title: id, Type: "task"}); err != nil {
					t.Fatalf("seed %q: %v", id, err)
				}
			}
			if tc.closed != "" {
				if err := store.Close(tc.closed); err != nil {
					t.Fatalf("close %q: %v", tc.closed, err)
				}
			}
			got, err := store.HasResidentOutside([]string{"gcg", "gcnq"})
			if err != nil {
				t.Fatalf("HasResidentOutside: %v", err)
			}
			if got != tc.want {
				t.Fatalf("HasResidentOutside = %v, want %v", got, tc.want)
			}
		})
	}
}

// An invalid bead_json must not affect the existence probe: this test fails if
// the census selects a full row and routes it through scanSQLiteBead/json.Unmarshal.
func TestSQLiteHasResidentOutsideDoesNotDecodeRows(t *testing.T) {
	store := openNamespaceCensusStore(t)
	if _, err := store.Create(Bead{ID: "ga-relic", Title: "ga-relic", Type: "task"}); err != nil {
		t.Fatalf("seed relic: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE beads SET bead_json='not-json' WHERE id=?`, "ga-relic"); err != nil {
		t.Fatalf("corrupt test row: %v", err)
	}
	got, err := store.HasResidentOutside([]string{"gcg"})
	if err != nil {
		t.Fatalf("HasResidentOutside decoded or otherwise read bead_json: %v", err)
	}
	if !got {
		t.Fatal("the malformed foreign row was not found")
	}
}

func TestSQLiteStoreIsNamespaceCensus(t *testing.T) {
	var store Store = openNamespaceCensusStore(t)
	if _, ok := store.(NamespaceCensus); !ok {
		t.Fatalf("%T does not satisfy NamespaceCensus", store)
	}
}
