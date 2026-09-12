package config

import "testing"

// cr-8eagyh. Runtime and provider are independent: a lane should move to Nomad by
// naming its runtime, not by hand-copying a wrapper provider that restates argv,
// hook support and prompt delivery as if they were properties of the agent.
//
// Measured before this existed: a lane whose provider was not pre-shaped got a
// live box, a live tmux server, a pane holding a bare shell, no agent, and no
// error anywhere.
func TestApplyRuntimeProviderShape(t *testing.T) {
	newProvider := func() *ResolvedProvider {
		return &ResolvedProvider{
			Name:              "qwen38-rtxpro6000",
			Args:              []string{"--dangerously-skip-permissions", "--model", "qwen"},
			PromptMode:        "arg",
			SupportsHooks:     true,
			ReadyPromptPrefix: "› Ask Codex",
			Env:               map[string]string{"GC_MODEL": "qwen38-next"},
		}
	}

	t.Run("nomad imposes the box shape", func(t *testing.T) {
		p := newProvider()
		ApplyRuntimeProviderShape(p, NomadRuntimeProvider)
		if p.Args != nil {
			t.Errorf("Args = %v, want nil — argv never reaches the agent in a box", p.Args)
		}
		if p.SupportsHooks {
			t.Error("SupportsHooks = true, want false — gc's hooks are not installed in the box")
		}
		if p.PromptMode != "none" {
			t.Errorf("PromptMode = %q, want \"none\" — priming cannot ride argv here", p.PromptMode)
		}
		// The provider's OWN facts must survive: these hold in both runtimes, and
		// clobbering them would break readiness detection and the model endpoint.
		if p.ReadyPromptPrefix != "› Ask Codex" {
			t.Errorf("ReadyPromptPrefix = %q — that is a provider fact and must survive", p.ReadyPromptPrefix)
		}
		if p.Env["GC_MODEL"] != "qwen38-next" {
			t.Errorf("Env lost the provider's model binding: %v", p.Env)
		}
	})

	t.Run("local is untouched", func(t *testing.T) {
		p := newProvider()
		ApplyRuntimeProviderShape(p, "local")
		if len(p.Args) != 3 || p.PromptMode != "arg" || !p.SupportsHooks {
			t.Fatalf("a non-Nomad runtime must be a no-op; got args=%v promptMode=%q hooks=%v",
				p.Args, p.PromptMode, p.SupportsHooks)
		}
	})

	t.Run("empty runtime selection is untouched", func(t *testing.T) {
		p := newProvider()
		ApplyRuntimeProviderShape(p, "")
		if len(p.Args) != 3 {
			t.Fatalf("no runtime selected must not impose a box shape; got args=%v", p.Args)
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		p := newProvider()
		ApplyRuntimeProviderShape(p, NomadRuntimeProvider)
		ApplyRuntimeProviderShape(p, NomadRuntimeProvider)
		if p.Args != nil || p.PromptMode != "none" || p.SupportsHooks {
			t.Fatal("applying twice must not differ from applying once")
		}
	})

	t.Run("nil provider does not panic", func(_ *testing.T) {
		ApplyRuntimeProviderShape(nil, NomadRuntimeProvider)
	})
}
