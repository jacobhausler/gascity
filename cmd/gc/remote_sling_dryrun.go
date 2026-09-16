package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/gastownhall/gascity/internal/api"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/sling"
)

// cmdSlingRemoteDryRun is the read-only pre-flight for
// `gc sling <target> <bead> --dry-run` against a REMOTE city. It issues GETs
// only — never the sling POST — so it cannot write anything.
//
// A dry-run exists so an operator can pre-flight a route before committing to
// it, and the two questions that break a sling silently are (1) does the target
// exist on THAT city and (2) does the target read the store the bead lives in
// (a cross-store route silently wedges pools, which is why the server-side
// validateBuiltInRouteStoreReachable guard exists). Both are answerable from
// the control plane's own read surface: the bead, the status agent table, and
// the rig table.
//
// Everything resolves from the REMOTE city as its own API serves it. Local
// config is never consulted, because the hazard this widening sits inside is a
// remote target whose local config is not the config of the city being routed
// in.
//
// Scope is the plain-bead route only: a formula launch has no read-only preview
// on this control plane (the sling wire carries no dry_run field), so --dry-run
// with a formula keeps its refusal in cmdSlingRemote.

// remoteSlingPreview is the rendered outcome of a remote dry-run.
type remoteSlingPreview struct {
	Target      string `json:"target"`
	Bead        string `json:"bead"`
	BeadType    string `json:"bead_type,omitempty"`
	BeadStatus  string `json:"bead_status,omitempty"`
	StoreRef    string `json:"store_ref,omitempty"`
	WouldSet    string `json:"would_set,omitempty"`
	Idempotency string `json:"idempotent,omitempty"`
}

// cmdSlingRemoteDryRun runs the pre-flight and renders the outcome. It returns
// 0 when the pre-flight says the sling would proceed, and 1 when it says the
// sling would refuse — or when the pre-flight itself could not be answered.
// Neither failure shape writes anything.
func cmdSlingRemoteDryRun(c *api.Client, target *remoteTarget, args []string, isFormula, force, jsonOutput bool, stdout, stderr io.Writer) int {
	fail := func(code, message string) int {
		if jsonOutput {
			return writeJSONError(stdout, stderr, code, message, 1)
		}
		fmt.Fprintln(stderr, message) //nolint:errcheck // best-effort stderr
		return 1
	}

	if isFormula {
		return fail("unsupported_remote", "gc sling: --dry-run with a formula is not supported for a remote city (a workflow launch has no read-only preview on the control plane); dry-run a bead route")
	}
	if len(args) != 2 {
		return fail("invalid_arguments", "gc sling: a remote city requires an explicit target and an existing bead: gc sling <target> <bead>")
	}
	targetArg, beadID := args[0], strings.TrimSpace(args[1])

	beadPrefix, _, ok := sling.BeadIDParts(beadID)
	if !ok {
		return fail("invalid_arguments", "gc sling: "+beadID+" is not a bead ID; a remote dry-run pre-flights an existing bead")
	}

	// Read 1: the bead. A sling whose bead the city cannot read fails on the
	// server (validateExistingBead), so an unreadable bead is a refusal here too.
	read, err := c.GetBead(beadID)
	if err != nil {
		return fail("bead_unreadable", fmt.Sprintf("gc sling: cannot pre-flight: city %s could not read bead %s: %v", target.CityName, beadID, err))
	}
	b := read.Body

	// Read 2: the agent table, so the target is validated against the remote
	// city's own config rather than a local guess.
	statusRead, statusErr := c.GetStatus()
	// Read 3: the rig table (name + bead prefix), which is what maps a bead
	// prefix to the store that holds it on THAT city.
	rigsRead, rigsErr := c.ListRigs()

	var notes []string
	if statusErr != nil {
		notes = append(notes, fmt.Sprintf("target not verified: the remote status read failed (%v)", statusErr))
	}
	if rigsErr != nil {
		notes = append(notes, fmt.Sprintf("store agreement not verified: the remote rig table read failed (%v)", rigsErr))
	}

	rt := remoteSlingRouteTarget{arg: targetArg}
	if statusErr == nil {
		resolved, ambiguous := resolveRemoteSlingTarget(statusRead.Body, targetArg)
		switch {
		case ambiguous != "":
			return fail("ambiguous_target", "gc sling: "+ambiguous)
		case resolved.found:
			rt = resolved
		case statusRead.Body.Partial:
			// A partial snapshot is not an authoritative "no such target".
			notes = append(notes, fmt.Sprintf("target %q not present in a PARTIAL status snapshot; treated as unverified, not unknown", targetArg))
		default:
			return fail("unknown_target", fmt.Sprintf("gc sling: target %q is not an agent or pool of city %s", targetArg, target.CityName))
		}
	}

	preview := remoteSlingPreview{
		Target:     orUnknown(rt.qualified, targetArg),
		Bead:       b.ID,
		BeadType:   b.Type,
		BeadStatus: b.Status,
	}

	// Class gate, mirroring the server-side rule in DoSlingBatch: an epic is not
	// slingable at all. A container (convoy) is slingable but expands its
	// children server-side, so the preview labels it rather than implying a
	// single-bead route.
	container := beads.IsContainerType(b.Type)
	if b.Type == "epic" {
		return fail("invalid_bead_class", fmt.Sprintf("gc sling: bead %s is an epic; first-class support is for convoys only", b.ID))
	}

	if rigsErr == nil {
		storeRef, _ := remoteBeadStoreRef(rigsRead.Body, target.CityName, beadPrefix)
		preview.StoreRef = storeRef
		// Store agreement: the same predicate as
		// agentutil.AgentReachesWorkflowStore, fed from remote reads. The real
		// (non-dry) route runs it, so printing its refusal here is reporting
		// what the route would do, not inventing an extra rule.
		if rt.found && rt.knowsStore {
			if refusal := remoteCrossStoreRefusal(b.ID, storeRef, rt); refusal != nil {
				return fail("cross_store", refusal.Error())
			}
		}
		// Cross-rig guard: the real route runs it unless --force.
		if rt.found && rt.scope == "rig" && rt.rigPrefix != "" && !force && !strings.EqualFold(rt.rigPrefix, beadPrefix) {
			crossRig := &sling.CrossRigError{
				BeadID:     b.ID,
				BeadPrefix: beadPrefix,
				Target:     rt.qualified,
				RigPrefix:  rt.rigPrefix,
			}
			return fail("cross_rig", "gc sling: "+crossRig.Error())
		}
	}

	// Idempotency, read off the bead's own route key: the server short-circuits
	// a re-sling toward the target it already names.
	if routed := strings.TrimSpace(b.Metadata["gc.routed_to"]); routed != "" && rt.found && rt.matchesRouteKey(routed) {
		preview.Idempotency = "already routed to " + routed
	}

	preview.WouldSet = "gc.routed_to=" + targetArg
	if preview.Idempotency != "" {
		preview.WouldSet = "nothing (idempotent no-op)"
	}

	if jsonOutput {
		payload := map[string]any{
			"schema_version": "1",
			"success":        true,
			"status":         "dry_run",
			"target":         preview.Target,
			"bead_id":        preview.Bead,
			"dry_run":        true,
			"would_set":      preview.WouldSet,
			"not_verified":   remoteSlingNotVerified(force),
		}
		putIfSet(payload, "bead_type", preview.BeadType)
		putIfSet(payload, "bead_status", preview.BeadStatus)
		putIfSet(payload, "store_ref", preview.StoreRef)
		putIfSet(payload, "idempotent", preview.Idempotency)
		if container {
			payload["container"] = true
		}
		if len(notes) > 0 {
			payload["warnings"] = notes
		}
		enc, err := json.Marshal(payload)
		if err != nil {
			return fail("encode_failed", "gc sling: encoding preview: "+err.Error())
		}
		fmt.Fprintln(stdout, string(enc)) //nolint:errcheck // best-effort stdout
		return 0
	}

	w := func(format string, a ...any) { fmt.Fprintf(stdout, format, a...) } //nolint:errcheck // best-effort stdout
	w("Target: %s\n", preview.Target)
	if rt.scope != "" {
		w("        scope=%s%s\n", rt.scope, rt.stateSuffix())
	}
	w("Work:   %s (%s, %s)\n", preview.Bead, orUnknown(preview.BeadType, "unknown"), orUnknown(preview.BeadStatus, "unknown"))
	if preview.StoreRef != "" {
		w("        store: %s\n", preview.StoreRef)
	}
	if container {
		w("        class: container; the real sling batch-routes its open children (child expansion is server-side)\n")
	}
	if preview.Idempotency != "" {
		w("Route:  %s; the real sling would short-circuit as a no-op\n", preview.Idempotency)
	} else {
		w("Route:  would write %s\n", preview.WouldSet)
	}
	for _, note := range notes {
		fmt.Fprintln(stderr, "warning:", note) //nolint:errcheck // best-effort stderr
	}
	for _, line := range remoteSlingNotVerified(force) {
		w("Not checked here: %s\n", line)
	}
	w("No side effects executed (--dry-run).\n")
	return 0
}

// remoteSlingRouteTarget is the pre-flight's view of a sling target, resolved
// from the remote city's status agent table.
type remoteSlingRouteTarget struct {
	arg        string
	name       string
	qualified  string
	scope      string // "city" | "rig"
	rig        string // rig name of a rig-scoped target
	rigPrefix  string // bead prefix of that rig, when the rig table resolved it
	knowsStore bool   // the target's own readable store is known
	suspended  bool
	running    bool
	found      bool
}

// matchesRouteKey reports whether a gc.routed_to value names this target. A pool
// is addressed by its pool name and a rig agent by its qualified or bare name,
// so all three spellings count.
func (t remoteSlingRouteTarget) matchesRouteKey(routed string) bool {
	for _, candidate := range []string{t.arg, t.name, t.qualified} {
		if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), routed) {
			return true
		}
	}
	return false
}

func (t remoteSlingRouteTarget) stateSuffix() string {
	switch {
	case t.suspended:
		return ", SUSPENDED"
	case t.running:
		return ", running"
	default:
		return ""
	}
}

// resolveRemoteSlingTarget resolves a sling target against the remote city's
// agent table, matching the qualified name, the bare agent/pool name, and the
// pool (group) name — the identifiers a local sling accepts. The second return
// is a refusal message when the target matches more than one distinct routing
// identity.
func resolveRemoteSlingTarget(status api.StatusView, target string) (remoteSlingRouteTarget, string) {
	target = strings.TrimSpace(target)
	var matches []remoteSlingRouteTarget
	for _, a := range status.Agents {
		if !strings.EqualFold(a.Name, target) &&
			!strings.EqualFold(a.QualifiedName, target) &&
			!strings.EqualFold(a.GroupName, target) {
			continue
		}
		m := remoteSlingRouteTarget{
			arg:        target,
			name:       a.Name,
			qualified:  a.QualifiedName,
			scope:      strings.TrimSpace(a.Scope),
			suspended:  a.Suspended,
			running:    a.Running,
			found:      true,
			knowsStore: strings.TrimSpace(a.Scope) != "",
		}
		if m.scope == "rig" {
			if rig, _, cut := strings.Cut(a.QualifiedName, "/"); cut {
				m.rig = rig
			}
		}
		matches = append(matches, m)
	}
	if len(matches) == 0 {
		return remoteSlingRouteTarget{arg: target}, ""
	}
	for _, m := range matches[1:] {
		if !strings.EqualFold(m.scope, matches[0].scope) || !strings.EqualFold(m.rig, matches[0].rig) {
			return remoteSlingRouteTarget{arg: target}, fmt.Sprintf(
				"target %q is ambiguous (matches %q and %q on this city); sling by qualified name",
				target, matches[0].qualified, m.qualified)
		}
	}
	return matches[0], ""
}

// remoteBeadStoreRef resolves which workflow store holds a bead prefix from the
// remote city's own rig table: a prefix a rig claims lives in "rig:<name>",
// anything else lives in the city store. It mirrors the server's
// slingStoreScopeForBead (rig prefix match, otherwise city) without needing the
// server's config object.
func remoteBeadStoreRef(rigs []api.RigView, cityName, beadPrefix string) (storeRef, rigName string) {
	for _, r := range rigs {
		if r.Prefix != "" && strings.EqualFold(r.Prefix, beadPrefix) {
			return "rig:" + r.Name, r.Name
		}
	}
	cn := strings.TrimSpace(cityName)
	if cn == "" {
		cn = "city"
	}
	return "city:" + cn, ""
}

// remoteCrossStoreRefusal reproduces agentutil.AgentReachesWorkflowStore over
// the fields the control plane exposes: a city-scoped target is cross-store
// eligible and reaches any store; a rig-scoped target reaches only its own rig
// store. The refusal is rendered by the domain's own typed error, so the
// operator reads the same diagnostic the real sling prints.
func remoteCrossStoreRefusal(beadID, storeRef string, t remoteSlingRouteTarget) error {
	if !strings.EqualFold(t.scope, "rig") {
		return nil // city-scoped targets are cross-store eligible
	}
	if t.rig == "" {
		return nil // cannot tell; the caller reports the check as unverified
	}
	if storeRef == "rig:"+t.rig {
		return nil
	}
	return &sling.CrossStoreRouteError{
		BeadID:            beadID,
		StoreRef:          storeRef,
		Target:            t.qualified,
		ReachableStoreRef: "rig:" + t.rig,
	}
}

// remoteSlingNotVerified names what this pre-flight cannot answer, so a green
// dry-run is never read as a full guarantee: the dependency-cycle check and any
// formula/workflow expansion both run server-side.
func remoteSlingNotVerified(force bool) []string {
	lines := []string{
		"dependency cycle (server-side)",
		"formula / workflow launch detail (server-side)",
	}
	if force {
		lines = append(lines, "--force: the real sling skips the cross-rig guard")
	}
	return lines
}

// orUnknown keeps a rendered row honest when a read returned an empty field.
func orUnknown(val, fallback string) string {
	if strings.TrimSpace(val) == "" {
		return fallback
	}
	return val
}
