package main

import (
	"io"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/session"
)

// sessionCurrentClaimFrontDoor opens the routed session-class front door for
// the city this process is running in. It is the shared root for the current-
// claim snapshot/reservation/clear operations and for `gc hook current` reads.
//
// It routes through cliSessionFrontDoor so a [beads.classes.sessions] relocation
// reaches both halves — a raw work-store front door would stamp the claim onto
// the work store while the real session bead lives in the relocated store, and
// `gc hook current` would then read back nothing forever. The no-refresh config
// loader matches the other hook-path roots (cmd_prime.go's
// persistPrimeHookProviderSessionKey): this runs on every claim, and a nil cfg
// leaves cliSessionStore identity to the input store.
func sessionCurrentClaimFrontDoor() (*session.Store, error) {
	cityPath, err := resolveCity()
	if err != nil {
		return nil, err
	}
	store, err := openCityStoreAt(cityPath)
	if err != nil {
		return nil, err
	}
	cfg, _ := loadCityConfigWithoutBuiltinPackRefresh(cityPath, io.Discard)
	return cliSessionFrontDoor(store, cfg, cityPath), nil
}

// hookReadCurrentSessionClaim snapshots the session-side current-claim pointer
// through the relocation-aware session front door. The snapshot is taken once
// before the work query; callers use it as the expected value for the later CAS.
func hookReadSessionCurrentClaim(sessionID string) (string, error) {
	sessFront, err := sessionCurrentClaimFrontDoor()
	if err != nil {
		return "", err
	}
	return sessFront.CurrentClaimBeadID(strings.TrimSpace(sessionID))
}

// hookReserveSessionCurrentClaim conditionally reserves next on the session
// bead. A missing or unsupported metadata CAS is returned as an error and never
// degraded to SetCurrentClaim.
func hookReserveSessionCurrentClaim(sessionID, expected, next string) (beads.MetadataCASOutcome, error) {
	sessFront, err := sessionCurrentClaimFrontDoor()
	if err != nil {
		return "", err
	}
	return sessFront.ReserveCurrentClaim(strings.TrimSpace(sessionID), expected, next)
}

// hookClearSessionCurrentClaimCAS conditionally clears expected from the
// session pointer, so an EPIPE unwind cannot erase a later winner.
func hookClearSessionCurrentClaimCAS(sessionID, expected string) (beads.MetadataCASOutcome, error) {
	sessFront, err := sessionCurrentClaimFrontDoor()
	if err != nil {
		return "", err
	}
	return sessFront.ClearCurrentClaim(strings.TrimSpace(sessionID), expected)
}
