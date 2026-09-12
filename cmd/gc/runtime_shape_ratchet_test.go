package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A RATCHET, not a pass/fail gate, and the distinction is the point.
//
// Twelve production call sites compose a launch or resume command from a
// ResolvedProvider. Exactly ONE applied the runtime's shape, and it applied it
// too late even there. The measured cost: a lane declaring
// runtime_provider="nomad" shipped its provider's args_append into every box,
// which ran `codex exec` with no prompt and died in the same second, for days.
//
// Converting all eleven at once is a large change through five files, several
// of which do not have the lane's runtime in scope and would need it threaded
// in. Doing that in one pass, on the same day the lane came back up, is how a
// fix becomes an outage.
//
// So this records the sites that are STILL unshaped, per file, across BOTH
// cmd/gc and internal/api, and fails when the number changes in either
// direction:
//
//   - a NEW unshaped launch site cannot be added silently — the count goes up
//     and this test names the file
//   - a conversion must come here and lower the number, so the remaining work
//     is visible in code rather than in a transcript someone has to remember
//
// It deliberately does not assert zero. Asserting zero today would mean either
// skipping the test or shipping eleven risky conversions to satisfy it, and a
// disabled guard protects nothing.
var unshapedLaunchSites = map[string]int{
	// cmdSessionNew's direct-start fallback, and buildResumeCommand's two
	// (CLI `gc session attach` / resume).
	"cmd_session.go": 4,
	// materializeSessionForTemplateWithOptions and
	// materializeSessionForAgentConfig. Both already compute the lane's runtime
	// (AgentRuntimeProviderOverrideValue) and then discard it before composing
	// the command, so these two are the cheapest conversions available.
	"session_template_start.go": 0,
	// worker_handle.go: THREE remain, and the count is not a to-do list of
	// three conversions. Two of them (the resolveTransport closure) return a
	// transport STRING and never compose a launch command, so shaping them
	// would be wrong — they are correctly unshaped and counted here only
	// because this guard counts bare calls per file rather than classifying
	// each one. The third resolves a provider BY NAME with no agent, so there
	// is no lane whose runtime could be imposed.
	//
	// The one that mattered — resolveWorkerRuntimeProviderWithConfigAndMetadata,
	// the resume/reattach path — is converted: a session RESUMED onto Nomad was
	// getting its provider's args_append back, the same defect that darkened a
	// lane for days on the reconciler path.
	"worker_handle.go": 3,

	// THE API PATHS, and the reason they are here is a correction. The first
	// version of this ratchet covered only cmd/gc and therefore counted ten
	// calls in three files while claiming to hold "the eleven" — a guard
	// narrower than the thing it guards, which is the same failure as a gate
	// coverage test that reads comments or an assertion that measures source
	// text. Caught by checking this map against the enumeration it was built
	// from instead of trusting it.
	//
	// These matter as much as the CLI ones: resolveSessionTemplateForCreate
	// backs the HTTP session-create handler's "agent" kind, and
	// resolveSessionRuntimeWithMetadata backs session RESUME. A lane routed to
	// Nomad through a dashboard create or a resume reproduces the defect
	// exactly.
	"../../internal/api/session_runtime.go":                4,
	"../../internal/api/handler_session_create.go":         1,
	"../../internal/api/huma_handlers_sessions_command.go": 1,
}

var resolveProviderCall = regexp.MustCompile(`config\.ResolveProvider\(`)

func TestUnshapedLaunchSitesDoNotGrow(t *testing.T) {
	for file, want := range unshapedLaunchSites {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("%s: %v — if this file was renamed, update the ratchet rather than dropping the entry", file, err)
			continue
		}
		got := len(resolveProviderCall.FindAllString(stripGoComments(string(body)), -1))
		switch {
		case got > want:
			t.Errorf("%s: %d bare config.ResolveProvider calls, ratchet says %d. A NEW launch path that "+
				"does not apply the runtime shape reproduces the defect that darkened a lane: a lane "+
				"naming its runtime gets its provider's argv anyway. Use "+
				"config.ResolveProviderForLaunch, or raise the number here and say why.", file, got, want)
		case got < want:
			t.Errorf("%s: %d bare config.ResolveProvider calls, ratchet says %d. Lower the ratchet — "+
				"the point of it is that the remaining work is counted.", file, got, want)
		}
	}
}

// stripGoComments keeps a commented-out or merely discussed call from counting
// as a live one. Learned the same day from the gate-4 coverage guard in the
// pack, which read whole files including comments and therefore could not fail
// for any of its twelve causes.
func stripGoComments(src string) string {
	var b strings.Builder
	for len(src) > 0 {
		switch {
		case strings.HasPrefix(src, "//"):
			if i := strings.IndexByte(src, '\n'); i >= 0 {
				src = src[i:]
			} else {
				src = ""
			}
		case strings.HasPrefix(src, "/*"):
			if i := strings.Index(src, "*/"); i >= 0 {
				src = src[i+2:]
			} else {
				src = ""
			}
		default:
			b.WriteByte(src[0])
			src = src[1:]
		}
	}
	return b.String()
}

// The ratchet is worthless if it points at files that no longer exist, so this
// fails loudly rather than silently counting zero in a renamed world.
func TestRatchetFilesExist(t *testing.T) {
	for file := range unshapedLaunchSites {
		if _, err := os.Stat(filepath.Clean(file)); err != nil {
			t.Errorf("ratchet names %s, which is not here: %v", file, err)
		}
	}
}
