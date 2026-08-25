package main

import (
	"fmt"
	"io"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/spf13/cobra"
)

// graphMembershipJSONResult is the bounded wire projection for a molecule's
// direct membership. It keeps the root separate from workflow rows so callers
// can reason about the root's recipe and each row's ownership metadata without
// guessing which row is the root. The rows are dedicated wire types: beads.Bead
// is the domain model and must not become an accidental CLI contract.
type graphMembershipJSONResult struct {
	SchemaVersion string                `json:"schema_version"`
	OK            bool                  `json:"ok"`
	RootID        string                `json:"root_id"`
	Membership    string                `json:"membership"`
	Limit         int                   `json:"limit"`
	Total         int                   `json:"total"`
	Truncated     bool                  `json:"truncated"`
	Root          *graphMembershipBead  `json:"root"`
	Members       []graphMembershipBead `json:"members"`
}

// graphMembershipBead is the stable subset needed by graph context consumers.
// In particular Metadata is retained because cleanup ownership and fences are
// bead facts, not a table of ids maintained by the consumer.
type graphMembershipBead struct {
	ID          string            `json:"id"`
	Title       string            `json:"title,omitempty"`
	Status      string            `json:"status"`
	Type        string            `json:"issue_type,omitempty"`
	ParentID    string            `json:"parent,omitempty"`
	Ref         string            `json:"ref,omitempty"`
	Description string            `json:"description,omitempty"`
	Labels      []string          `json:"labels,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

func newGraphMembershipCmd(stdout, stderr io.Writer) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "membership <root-id>",
		Short: "Show bounded direct workflow membership for a graph root",
		Long: `Show the root and its direct workflow members.

Membership is defined by gc.root_bead_id metadata and spans active and closed
rows. The result is always structured JSON and includes a truncation marker;
callers must reject truncated results when they need a complete workflow.

The read routes the root through the graph class binding before applying
beads.DirectMembers, so a relocated graph root and its members are read from
the owning store rather than from the work ledger alone.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if code := cmdGraphMembership(args[0], limit, stdout, stderr); code != 0 {
				return errExit
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 64, "maximum number of root and member rows to emit")
	// This surface is intentionally JSON-only. Keep --json as an explicit
	// spelling for scripts and parity with `gc graph`; the result is structured
	// even when the flag is omitted.
	cmd.Flags().Bool("json", false, "output structured JSON (the default)")
	return cmd
}

func cmdGraphMembership(rootID string, limit int, stdout, stderr io.Writer) int {
	cityPath, err := resolveCity()
	if err != nil {
		fmt.Fprintf(stderr, "gc graph membership: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	store, code := openRigAwareStore([]string{rootID}, stderr)
	if code != 0 || store == nil {
		return code
	}
	return doGraphMembership(graphStoresFor(store, cityPath), rootID, limit, stdout, stderr)
}

func doGraphMembership(stores *graphStores, rootID string, limit int, stdout, stderr io.Writer) int {
	if stores == nil || stores.work == nil {
		fmt.Fprintln(stderr, "gc graph membership: bead store is unavailable") //nolint:errcheck // best-effort stderr
		return 1
	}
	if limit < 1 {
		fmt.Fprintln(stderr, "gc graph membership: --limit must be positive") //nolint:errcheck // best-effort stderr
		return 1
	}
	if rootID == "" {
		fmt.Fprintln(stderr, "gc graph membership: missing root ID") //nolint:errcheck // best-effort stderr
		return 1
	}

	rootStore, err := stores.storeFor(rootID)
	if err != nil {
		fmt.Fprintf(stderr, "gc graph membership: resolving the store for %s: %v\n", rootID, err) //nolint:errcheck // best-effort stderr
		return 1
	}
	all, err := beads.DirectMembers(rootStore, rootID)
	if err != nil {
		fmt.Fprintf(stderr, "gc graph membership: reading %s: %v\n", rootID, err) //nolint:errcheck // best-effort stderr
		return 1
	}

	emitted := all
	truncated := false
	if len(emitted) > limit {
		emitted = emitted[:limit]
		truncated = true
	}
	result := graphMembershipJSONResult{
		SchemaVersion: "1",
		OK:            true,
		RootID:        rootID,
		Membership:    beads.MembershipDirectRootID.String(),
		Limit:         limit,
		Total:         len(emitted),
		Truncated:     truncated,
		Members:       make([]graphMembershipBead, 0, len(emitted)),
	}
	for _, bead := range emitted {
		row := graphMembershipBeadFrom(bead)
		if bead.ID == rootID {
			root := row
			result.Root = &root
			continue
		}
		result.Members = append(result.Members, row)
	}
	return writeCLIJSONLineOrExit(stdout, stderr, "gc graph membership", result)
}

func graphMembershipBeadFrom(bead beads.Bead) graphMembershipBead {
	return graphMembershipBead{
		ID:          bead.ID,
		Title:       bead.Title,
		Status:      bead.Status,
		Type:        bead.Type,
		ParentID:    bead.ParentID,
		Ref:         bead.Ref,
		Description: bead.Description,
		Labels:      append([]string(nil), bead.Labels...),
		Metadata:    cloneGraphStringMap(bead.Metadata),
	}
}

func cloneGraphStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}
