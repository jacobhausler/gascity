package doctor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/worker/transcript"
)

// ProviderParityCheck flags providers used by configured agents whose
// capability fields are likely to silently no-op at runtime. The signal
// is structural — it inspects each provider after applying the same
// config resolution semantics used for runtime sessions, and warns when:
//
//   - ResumeFlag and ResumeCommand are both empty: every session restart
//     will silently drop the session-id and start a fresh process
//     (resolveResumeCommand short-circuits, gap 1 of #672).
//   - No route exists to a provider conversation id (no session_id_flag, and
//     no lifecycle hook that could report one back) for a family whose
//     transcript lookup is keyed by such an id. gc then persists no
//     session_key, the keyed lookup misses, and the transcript view silently
//     serves the provider-neutral text fallback. That is a capability gap,
//     distinguishable from "this session's transcript is missing"
//     (gastownhall/gascity#6083). transcript.ProviderConversationKeyGap is
//     the one predicate, shared with the launch-time warning so the two can
//     never disagree.
//
// A provider with SupportsHooks is NOT flagged on this axis either: its own
// SessionStart hook reports the conversation id back to gc prime --hook, and
// that is a second real route to the key.
//
// SupportsHooks=false is intentionally NOT flagged — many providers
// genuinely lack a hook surface and Gas Town has an alternative drain
// path (NeedsNudgePoller) for them. Flagging it would be noise.
//
// Each warning names the provider, what is missing, and the consequence.
type ProviderParityCheck struct {
	cfg *config.City
}

// NewProviderParityCheck creates a check that flags provider capability
// gaps for every provider referenced by a configured agent.
func NewProviderParityCheck(cfg *config.City) *ProviderParityCheck {
	return &ProviderParityCheck{cfg: cfg}
}

// Name returns the check identifier.
func (c *ProviderParityCheck) Name() string { return "provider-parity" }

// Run inspects each provider referenced by at least one agent and
// reports capability gaps as warnings.
func (c *ProviderParityCheck) Run(_ *CheckContext) *CheckResult {
	r := &CheckResult{Name: c.Name()}
	if c.cfg == nil {
		r.Status = StatusOK
		r.Message = "no config; nothing to check"
		return r
	}

	providers := providersInUse(c.cfg)
	if len(providers) == 0 {
		r.Status = StatusOK
		r.Message = "no providers referenced by configured agents"
		return r
	}

	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)

	var details []string
	for _, name := range names {
		provider := providers[name]
		if strings.TrimSpace(provider.ResumeFlag) == "" && strings.TrimSpace(provider.ResumeCommand) == "" {
			details = append(details, fmt.Sprintf(
				"provider %q has no ResumeFlag or ResumeCommand: session restarts will silently drop the session-id and start a fresh process",
				name,
			))
		}
		// The continuity gap needs both halves: no id gc can assign, AND a
		// discovery layer that will key its lookup on such an id. A family whose
		// discovery is work-dir based still finds the transcript, so flagging it
		// would be noise about a gap that costs nothing.
		// One shared predicate with the launch-time warning: the gap needs no
		// route to a provider conversation id AND a discovery layer that keys on
		// one. Hook-managed providers report their id to `gc prime --hook`, and
		// work-dir-keyed families find their transcript without an id, so neither
		// is flagged -- see transcript.ProviderConversationKeyGap.
		if transcript.ProviderConversationKeyGap(provider) {
			details = append(details, fmt.Sprintf(
				"provider %q has no session_id_flag: gc cannot assign it a conversation id, so no session_key is persisted and its transcript view falls back to provider-neutral text (a capability gap, not a missing transcript)",
				name,
			))
		}
	}

	if len(details) == 0 {
		r.Status = StatusOK
		r.Message = "provider capabilities look complete"
		return r
	}
	r.Status = StatusWarning
	r.Message = fmt.Sprintf("%d provider capability gap(s)", len(details))
	r.Details = details
	r.FixHint = "populate resume_flag (or resume_command) in the provider spec; where the provider CLI accepts a caller-supplied conversation id, also populate session_id_flag, or ship a session-start hook that reports the id back (never a flag the provider rejects - it is inert at best and breaks resume at worst). See internal/config/provider.go for the built-in presets and gastownhall/gascity#672 / #6083 (non-Claude provider parity)"
	return r
}

// CanFix returns false — provider capability fields are config-managed.
func (c *ProviderParityCheck) CanFix() bool { return false }

// Fix is a no-op.
func (c *ProviderParityCheck) Fix(_ *CheckContext) error { return nil }

// providersInUse returns the fully resolved provider for every provider
// used by at least one configured agent. It delegates provider inheritance
// and agent-level overrides to config.ResolveProvider, using a PATH lookup
// stub because provider-parity checks config semantics rather than host
// binaries. Agents that pin StartCommand are skipped because they bypass
// ProviderSpec.
//
// When the same provider name is referenced by multiple agents, the result
// prefers the entry whose resume capability is *missing* — that is the
// signal worth surfacing.
func providersInUse(cfg *config.City) map[string]config.ResolvedProvider {
	out := map[string]config.ResolvedProvider{}

	addResolved := func(agent config.Agent) {
		resolved, err := config.ResolveProvider(&agent, &cfg.Workspace, cfg.Providers, providerParityLookPath)
		if err != nil {
			return
		}
		name := strings.TrimSpace(resolved.Name)
		if name == "" {
			return
		}
		hasResume := strings.TrimSpace(resolved.ResumeFlag) != "" || strings.TrimSpace(resolved.ResumeCommand) != ""
		current, seen := out[name]
		currentHasResume := strings.TrimSpace(current.ResumeFlag) != "" || strings.TrimSpace(current.ResumeCommand) != ""
		if !seen || (currentHasResume && !hasResume) {
			out[name] = *resolved
		}
	}

	checkedAgentProvider := false
	for _, a := range cfg.Agents {
		if a.StartCommand != "" {
			continue
		}
		if strings.TrimSpace(a.Provider) == "" && strings.TrimSpace(cfg.Workspace.Provider) == "" {
			continue
		}
		checkedAgentProvider = true
		addResolved(a)
	}
	if !checkedAgentProvider && len(cfg.Agents) == 0 && strings.TrimSpace(cfg.Workspace.Provider) != "" {
		addResolved(config.Agent{})
	}
	return out
}

func providerParityLookPath(command string) (string, error) {
	return command, nil
}
