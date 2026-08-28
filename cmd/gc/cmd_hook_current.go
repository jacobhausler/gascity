package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gastownhall/gascity/internal/session"
)

// hookCurrentSessionFrontDoor is the session-front-door seam `gc hook current`
// reads through, overridable in tests.
var hookCurrentSessionFrontDoor = sessionCurrentClaimFrontDoor

func newHookCurrentCmd(stdout, stderr io.Writer) *cobra.Command {
	var idOnly bool
	cmd := &cobra.Command{
		Use:   "current",
		Short: "Print the work bead this session most recently claimed",
		Long: `Prints the work bead this session most recently claimed with gc hook --claim.

The claim protocol stamps the claimed bead id onto the calling session's own
bead, because a pool session's shell receives no bead id of its own: $GC_BEAD_ID
exists only in the controller's dispatch condition environment, and the demand
trigger is exported presence-only as $GC_SPAWN_ORIGIN=demand, never as an id. A
formula step that must close the bead it is running reads it back here:

    BEAD_ID="${GC_BEAD_ID:-$(gc hook current --id-only)}"

Put this command ahead of any controller-supplied hint in such a chain. A hint
describes why a seat was started; only the claim says what it is running, and a
chain that prefers the hint closes whatever the controller last counted.

The calling session is taken from $GC_SESSION_ID. Exits 1 when there is no
session identity and when the session has claimed nothing, so a caller that
cannot name its bead fails loudly instead of skipping its own work.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return exitForCode(cmdHookCurrent(idOnly, stdout, stderr))
		},
	}
	cmd.Flags().BoolVar(&idOnly, "id-only", false, "print only the bead id, with no surrounding context")
	return cmd
}

// cmdHookCurrent resolves the calling session from the environment and prints
// its current claim. It is the thin env+front-door root over doHookCurrent, which
// holds the whole decision so it can be exercised without a city on disk.
func cmdHookCurrent(idOnly bool, stdout, stderr io.Writer) int {
	sessionID := strings.TrimSpace(os.Getenv("GC_SESSION_ID"))
	if sessionID == "" {
		fmt.Fprintln(stderr, "gc hook current: no session identity (set $GC_SESSION_ID); only a session that claimed work has a current bead") //nolint:errcheck
		return 1
	}
	// A remote city answers the same question over the API. Resolved before the
	// local front door because opening that door resolves a city on disk, which
	// a box running off the controller's host does not have.
	client, isRemote, err := resolveWorkerTarget()
	if err != nil {
		fmt.Fprintf(stderr, "gc hook current: %v\n", err) //nolint:errcheck
		return 1
	}
	if isRemote {
		return remoteHookCurrent(client, sessionID, idOnly, stdout, stderr)
	}
	sessFront, err := hookCurrentSessionFrontDoor()
	if err != nil {
		fmt.Fprintf(stderr, "gc hook current: %v\n", err) //nolint:errcheck
		return 1
	}
	return doHookCurrent(sessFront, sessionID, idOnly, stdout, stderr)
}

// doHookCurrent prints the bead id stamped on sessionID's bead by the claim
// protocol. It exits 1 — never 0 with empty output — when nothing is stamped:
// the whole point of the back-channel is that a step which cannot name its own
// bead must fail loudly rather than let a caller substitute an empty string and
// skip the close it owes.
func doHookCurrent(sessFront *session.Store, sessionID string, idOnly bool, stdout, stderr io.Writer) int {
	beadID, err := sessFront.CurrentClaimBeadID(sessionID)
	if err != nil {
		fmt.Fprintf(stderr, "gc hook current: %v\n", err) //nolint:errcheck
		return 1
	}
	if beadID == "" {
		fmt.Fprintf(stderr, "gc hook current: session %s has no current claim (nothing claimed through gc hook --claim)\n", sessionID) //nolint:errcheck
		return 1
	}
	if idOnly {
		fmt.Fprintln(stdout, beadID) //nolint:errcheck
		return 0
	}
	fmt.Fprintf(stdout, "%s (claimed by session %s)\n", beadID, sessionID) //nolint:errcheck
	return 0
}
