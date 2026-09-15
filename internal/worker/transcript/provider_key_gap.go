package transcript

import (
	"strings"

	"github.com/gastownhall/gascity/internal/config"
)

// ProviderConversationKeyGap reports whether gc's transcript discovery will
// key its lookup on a provider conversation id that gc can never obtain for
// this provider — the structural condition behind a session that runs fine,
// looks healthy in `gc sessions`, and has no history to show (see
// gastownhall/gascity#6083). It is the answer to "is this provider's
// transcript degradation a capability gap rather than a missing transcript",
// so callers can report it out loud instead of letting it surface as an empty
// history.
//
// gc learns a provider conversation id by exactly two routes:
//
//   - it assigns one itself at fresh start, which needs the provider's
//     session_id_flag (config.ResolvedProvider.CanAssignSessionID);
//   - the provider's own lifecycle hook reports it back to `gc prime --hook`,
//     which is what a provider with SupportsHooks offers.
//
// A provider offering neither never gets a session_key persisted, so
// DiscoverKeyedPath can only miss. Families whose discovery does not need an
// id at all (SupportsIDLookup false — they are discovered by work dir or by a
// scope the adapter derives from gc's own identity) are therefore not in the
// gap: gc never needed the id there and still finds the transcript.
//
// Fully custom providers — one with no built-in ancestor in its resolution
// chain — answer false as well. gc cannot assume an on-disk layout it has
// never seen, and claiming a gap there would be a guess about somebody else's
// CLI; the same restraint keeps HasKeyedTranscript from treating an unknown
// provider's missing transcript as a stale resume key.
func ProviderConversationKeyGap(rp config.ResolvedProvider) bool {
	// BuiltinAncestor only, never Kind: the resolver fills Kind with the
	// provider's own name when nothing matched a built-in, so treating it as a
	// family would widen every custom provider into the match. config's rule
	// for family branches is BuiltinFamily / BuiltinAncestor, with "" meaning
	// "undetermined" — and an undetermined family is one gc has no transcript
	// layout to assume, so it cannot claim this provider is degraded.
	family := strings.TrimSpace(rp.BuiltinAncestor)
	if family == "" {
		return false
	}
	if rp.CanAssignSessionID() || rp.SupportsHooks {
		return false
	}
	return SupportsIDLookup(family)
}
