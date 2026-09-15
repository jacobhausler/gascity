package doctor

import (
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

func TestProviderParityCheck_NoConfig(t *testing.T) {
	c := NewProviderParityCheck(nil)
	r := c.Run(&CheckContext{})
	if r.Status != StatusOK {
		t.Fatalf("Status = %v, want StatusOK", r.Status)
	}
	if !strings.Contains(r.Message, "no config") {
		t.Errorf("Message = %q, want mention of no config", r.Message)
	}
}

func TestProviderParityCheck_NoAgents(t *testing.T) {
	cfg := &config.City{}
	r := NewProviderParityCheck(cfg).Run(&CheckContext{})
	if r.Status != StatusOK {
		t.Fatalf("Status = %v, want StatusOK; details=%v", r.Status, r.Details)
	}
	if !strings.Contains(r.Message, "no providers referenced") {
		t.Errorf("Message = %q, want mention of no providers", r.Message)
	}
}

func TestProviderParityCheck_ClaudeOnly(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{{Name: "mayor", Provider: "claude"}},
	}
	r := NewProviderParityCheck(cfg).Run(&CheckContext{})
	if r.Status != StatusOK {
		t.Fatalf("Status = %v, want StatusOK; details=%v", r.Status, r.Details)
	}
}

func TestProviderParityCheck_FlagsProviderWithoutResume(t *testing.T) {
	// Inject a city-defined provider that explicitly opts out of resume
	// (empty ResumeFlag + ResumeCommand) and reference it from an agent.
	cfg := &config.City{
		Agents: []config.Agent{{Name: "tester", Provider: "noresume"}},
		Providers: map[string]config.ProviderSpec{
			"noresume": {Command: "noresume"},
		},
	}
	r := NewProviderParityCheck(cfg).Run(&CheckContext{})
	if r.Status != StatusWarning {
		t.Fatalf("Status = %v, want StatusWarning; details=%v", r.Status, r.Details)
	}
	if len(r.Details) != 1 {
		t.Fatalf("Details = %d, want 1: %v", len(r.Details), r.Details)
	}
	if !strings.Contains(r.Details[0], `"noresume"`) || !strings.Contains(r.Details[0], "ResumeFlag") {
		t.Errorf("Details[0] missing provider name or ResumeFlag mention: %q", r.Details[0])
	}
	if r.FixHint == "" {
		t.Error("expected non-empty FixHint")
	}
}

func TestProviderParityCheck_BypassedWhenStartCommandSet(t *testing.T) {
	// Agents that pin StartCommand bypass ProviderSpec entirely, so the
	// provider parity warning should not fire.
	cfg := &config.City{
		Agents: []config.Agent{{Name: "raw", StartCommand: "raw --foo", Provider: "noresume"}},
		Providers: map[string]config.ProviderSpec{
			"noresume": {Command: "noresume"},
		},
	}
	r := NewProviderParityCheck(cfg).Run(&CheckContext{})
	if r.Status != StatusOK {
		t.Fatalf("Status = %v, want StatusOK; details=%v", r.Status, r.Details)
	}
}

func TestProviderParityCheck_ChecksWorkspaceDefaultProvider(t *testing.T) {
	cfg := &config.City{
		Workspace: config.Workspace{Provider: "noresume"},
		Providers: map[string]config.ProviderSpec{
			"noresume": {Command: "noresume"},
		},
	}
	r := NewProviderParityCheck(cfg).Run(&CheckContext{})
	if r.Status != StatusWarning {
		t.Fatalf("Status = %v, want StatusWarning; details=%v", r.Status, r.Details)
	}
}

func TestProviderParityCheck_CityOverrideExtendsBuiltin(t *testing.T) {
	// City-defined "claude" overrides only DisplayName; ResumeFlag inherits
	// from the builtin so the check should pass.
	cfg := &config.City{
		Agents: []config.Agent{{Name: "mayor", Provider: "claude"}},
		Providers: map[string]config.ProviderSpec{
			"claude": {DisplayName: "MyClaude"},
		},
	}
	r := NewProviderParityCheck(cfg).Run(&CheckContext{})
	if r.Status != StatusOK {
		t.Fatalf("Status = %v, want StatusOK; details=%v", r.Status, r.Details)
	}
}

func TestProviderParityCheck_CustomProviderBaseInheritsBuiltinResume(t *testing.T) {
	base := "builtin:codex"
	cfg := &config.City{
		Agents: []config.Agent{{Name: "coder", Provider: "wrapped-codex"}},
		Providers: map[string]config.ProviderSpec{
			"wrapped-codex": {Base: &base},
		},
	}
	r := NewProviderParityCheck(cfg).Run(&CheckContext{})
	if r.Status != StatusOK {
		t.Fatalf("Status = %v, want StatusOK; details=%v", r.Status, r.Details)
	}
}

func TestProviderParityCheck_CommandMatchInheritsBuiltinResume(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{{Name: "helper", Provider: "fast-claude"}},
		Providers: map[string]config.ProviderSpec{
			"fast-claude": {Command: "claude"},
		},
	}
	r := NewProviderParityCheck(cfg).Run(&CheckContext{})
	if r.Status != StatusOK {
		t.Fatalf("Status = %v, want StatusOK; details=%v", r.Status, r.Details)
	}
}

func TestProviderParityCheck_AgentResumeCommandSuppressesWarning(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{{
			Name:          "tester",
			Provider:      "noresume",
			ResumeCommand: "noresume --continue {{.SessionKey}}",
		}},
		Providers: map[string]config.ProviderSpec{
			"noresume": {Command: "noresume"},
		},
	}
	r := NewProviderParityCheck(cfg).Run(&CheckContext{})
	if r.Status != StatusOK {
		t.Fatalf("Status = %v, want StatusOK; details=%v", r.Status, r.Details)
	}
}

func TestProviderParityCheck_ExplicitStandaloneBuiltinNameWarns(t *testing.T) {
	base := ""
	cfg := &config.City{
		Agents: []config.Agent{{Name: "standalone", Provider: "claude"}},
		Providers: map[string]config.ProviderSpec{
			"claude": {Base: &base, Command: "claude"},
		},
	}
	r := NewProviderParityCheck(cfg).Run(&CheckContext{})
	if r.Status != StatusWarning {
		t.Fatalf("Status = %v, want StatusWarning; details=%v", r.Status, r.Details)
	}
	if len(r.Details) != 1 || !strings.Contains(r.Details[0], `"claude"`) {
		t.Fatalf("Details = %v, want one warning for claude", r.Details)
	}
}

func TestProviderParityCheck_DeterministicOrdering(t *testing.T) {
	// Multiple gaps must be reported in stable alphabetic order.
	cfg := &config.City{
		Agents: []config.Agent{
			{Name: "a1", Provider: "zproblem"},
			{Name: "a2", Provider: "aproblem"},
		},
		Providers: map[string]config.ProviderSpec{
			"zproblem": {Command: "z"},
			"aproblem": {Command: "a"},
		},
	}
	r := NewProviderParityCheck(cfg).Run(&CheckContext{})
	if r.Status != StatusWarning {
		t.Fatalf("Status = %v, want StatusWarning; details=%v", r.Status, r.Details)
	}
	if len(r.Details) != 2 {
		t.Fatalf("Details = %d, want 2: %v", len(r.Details), r.Details)
	}
	if !strings.Contains(r.Details[0], `"aproblem"`) {
		t.Errorf("Details[0] should mention aproblem first: %q", r.Details[0])
	}
	if !strings.Contains(r.Details[1], `"zproblem"`) {
		t.Errorf("Details[1] should mention zproblem second: %q", r.Details[1])
	}
}

// A provider with no route to a conversation id — no session_id_flag for gc to
// assign by, and no session-start hook that could report one back — silently
// loses transcript continuity and resume across restarts. #6083 went unnoticed
// across 94 sessions precisely because nothing said so; doctor must.
func TestProviderParityCheck_FlagsProviderWithNoConversationKeyRoute(t *testing.T) {
	base := "builtin:grok"
	cfg := &config.City{
		Agents: []config.Agent{{Name: "coder", Provider: "grok-worker"}},
		Providers: map[string]config.ProviderSpec{
			"grok-worker": {Base: &base},
		},
	}
	r := NewProviderParityCheck(cfg).Run(&CheckContext{})
	if r.Status != StatusWarning {
		t.Fatalf("Status = %v, want StatusWarning; details=%v", r.Status, r.Details)
	}
	if len(r.Details) != 1 {
		t.Fatalf("Details = %d, want 1 (grok has resume, so only the key gap): %v", len(r.Details), r.Details)
	}
	if !strings.Contains(r.Details[0], `"grok-worker"`) || !strings.Contains(r.Details[0], "session_id_flag") {
		t.Errorf("Details[0] must name the provider and the missing knob: %q", r.Details[0])
	}
	if !strings.Contains(r.Details[0], "capability gap") {
		t.Errorf("Details[0] must say this is a capability gap, not a missing transcript: %q", r.Details[0])
	}
}

// Hook-managed providers report their conversation id to `gc prime --hook`, so
// a missing session_id_flag costs them nothing and must not be flagged: a
// warning that fires on healthy continuity is worse than the silence #6083 was.
func TestProviderParityCheck_HookManagedProviderNotFlaggedForKeyRoute(t *testing.T) {
	for _, base := range []string{"builtin:codex", "builtin:opencode", "builtin:gemini"} {
		b := base
		cfg := &config.City{
			Agents: []config.Agent{{Name: "coder", Provider: "wrapped"}},
			Providers: map[string]config.ProviderSpec{
				"wrapped": {Base: &b},
			},
		}
		r := NewProviderParityCheck(cfg).Run(&CheckContext{})
		if r.Status != StatusOK {
			t.Errorf("base %s: Status = %v, want StatusOK; details=%v", base, r.Status, r.Details)
		}
	}
}
