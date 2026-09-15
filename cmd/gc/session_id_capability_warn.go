package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/worker/transcript"
)

// sessionIDContinuityWarnSink receives the launch-time provider-continuity
// warning. It is a package-level best-effort stderr writer — like the other
// start-prep diagnostics — because the paths that prepare a start (reconciler
// wake, restart handoff, warm-box relaunch) do not own a writer, and so tests
// can capture what the operator would see.
var sessionIDContinuityWarnSink io.Writer = os.Stderr

// sessionIDContinuityWarned remembers which (session, provider) pairs have
// already been reported in this process. Start preparation runs again on
// every wake and on every launch-only config change, while the gap is a
// property of the provider rather than of the launch: repeating it each time
// would be noise, not signal.
var sessionIDContinuityWarned sync.Map // session name + "\x00" + provider name

// warnSessionCannotAssignSessionID states out loud the degradation that
// otherwise passes unnoticed (gastownhall/gascity#6083). Some providers have
// no route at all to a provider conversation id: no session_id_flag for gc to
// assign one by, and no session-start hook that could report one back. gc
// therefore mints no session_key for them. Nothing errors, the pane runs, the
// session bead looks healthy — and what the operator eventually notices is
// that the session has no history, because a restart cannot reattach to the
// previous conversation and the keyed transcript lookup has no id to key on,
// so the transcript view serves the provider-neutral text fallback.
//
// transcript.ProviderConversationKeyGap is the predicate, so this fires for
// exactly the providers that are degraded and never for one that learns its id
// from a hook — a warning that fired on healthy continuity would be worse than
// the silence it replaces. The message is a capability statement rather than a
// per-session transcript complaint: it names the knob that closes the gap and
// the condition on that knob, because session_id_flag is inert unless the
// provider CLI accepts a caller-supplied id, and actively breaks the next
// restart on a provider that rejects it alongside its resume verb. That is why
// this only warns, and never mints.
func warnSessionCannotAssignSessionID(sessionName string, rp *config.ResolvedProvider) {
	if rp == nil || !transcript.ProviderConversationKeyGap(*rp) {
		return
	}
	providerName, command := "(none)", ""
	if rp != nil {
		if n := strings.TrimSpace(rp.Name); n != "" {
			providerName = n
		}
		command = strings.TrimSpace(rp.Command)
	}
	name := strings.TrimSpace(sessionName)
	if name == "" {
		name = "(unknown session)"
	}
	if _, seen := sessionIDContinuityWarned.LoadOrStore(name+"\x00"+providerName, true); seen {
		return
	}
	binary := command
	if binary == "" {
		binary = providerName
	}
	//nolint:errcheck // best-effort stderr
	fmt.Fprintf(sessionIDContinuityWarnSink,
		"gc session %s: provider %q has no route to a conversation id (no session_id_flag, and no session-start hook to report one), so gc mints no session_key: a restart starts a new conversation and the transcript view falls back to provider-neutral text. This is a provider capability gap, not a missing transcript (gastownhall/gascity#6083); set session_id_flag only if %s actually accepts such a flag.\n",
		name, providerName, binary)
}
