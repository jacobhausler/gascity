package config

import (
	"reflect"
	"testing"
)

// cr-jsxjcr: the nomad shape dropped ALL of a provider's argv, which is correct
// for orchestrator-shaped flags and fatal for agent-shaped ones. A provider that
// is started in one-shot exec mode declares `exec` plus its flags in
// args_append; with argv nulled every box rendered as the interactive TUI,
// answered one turn and parked at its prompt forever. `box_args = true` is the
// provider's opt-in to keep that argv; everything else is unchanged.

func TestApplyRuntimeProviderShapeBoxArgsKeepsAgentArgv(t *testing.T) {
	args := []string{
		"exec",
		"--skip-git-repo-check",
		"-c", "model=qwen3.8",
		"-c", "model_reasoning_effort=high",
	}
	provider := &ResolvedProvider{
		Command:     "codex",
		Args:        append([]string(nil), args...),
		PromptMode:  "arg",
		SupportsACP: true,
		BoxArgs:     true,
	}

	ApplyRuntimeProviderShape(provider, NomadRuntimeProvider)

	if !reflect.DeepEqual(provider.Args, args) {
		t.Errorf("Args = %v, want the provider's argv kept verbatim in order %v", provider.Args, args)
	}
	// The opt-in is about argv only. The runtime still owns prompt delivery and
	// hook support: gc does not prime the pane through this argv, and gc's hooks
	// are not installed inside the box.
	if provider.PromptMode != "none" {
		t.Errorf("PromptMode = %q, want \"none\" — the adapter primes from the nudge the start wire carries", provider.PromptMode)
	}
	if provider.SupportsHooks {
		t.Error("SupportsHooks = true, want false: gc's hooks are not installed in the box")
	}
	if !provider.SupportsACP {
		t.Error("SupportsACP = false, want untouched: that is a provider fact")
	}
}

func TestApplyRuntimeProviderShapeBoxArgsDropsHostModelProvider(t *testing.T) {
	// model_provider names a key in the HOST's catalog; the box authenticates
	// against the provider stanza internal/buildimage/codex.go bakes into its
	// own config.toml, so forwarding the host spelling asks the agent for a
	// provider its config does not define. Both -c spellings are filtered; the
	// settings the box does need survive.
	provider := &ResolvedProvider{
		Command: "codex",
		BoxArgs: true,
		Args: []string{
			"exec",
			"-c", "model_provider=host-gateway",
			"-cmodel_provider=fused-gateway",
			"-c", "model=qwen3.8",
			"-cmodel_reasoning_effort=high",
			"-c", // trailing flag with no value: passed through, not guessed about
		},
	}

	ApplyRuntimeProviderShape(provider, NomadRuntimeProvider)

	want := []string{
		"exec",
		"-c", "model=qwen3.8",
		"-cmodel_reasoning_effort=high",
		"-c",
	}
	if !reflect.DeepEqual(provider.Args, want) {
		t.Errorf("Args = %v, want %v", provider.Args, want)
	}
}

func TestApplyRuntimeProviderShapeWithoutBoxArgsStillDropsArgv(t *testing.T) {
	// The other half of the pin, and the reason the opt-in exists: the 2026-09-12
	// dark lane was every box launched as `codex exec` with no prompt because
	// args_append reached argv on a runtime that primes by other means. A lane
	// that does not opt in must stay exactly as shipped.
	provider := &ResolvedProvider{
		Command:    "codex",
		Args:       []string{"exec", "--skip-git-repo-check", "-c", "model=qwen3.8"},
		PromptMode: "arg",
	}

	ApplyRuntimeProviderShape(provider, NomadRuntimeProvider)

	if provider.Args != nil {
		t.Errorf("Args = %v, want nil for a provider that did not opt into box_args", provider.Args)
	}
	if provider.PromptMode != "none" {
		t.Errorf("PromptMode = %q, want \"none\"", provider.PromptMode)
	}
}

func TestApplyRuntimeProviderShapeBoxArgsIsNomadOnly(t *testing.T) {
	// Moving a lane back off Nomad recovers the provider's own argv with or
	// without the opt-in — the shape, and therefore the opt-in, is the
	// orchestrator boundary and nothing else.
	for _, runtimeName := range []string{"", "ssh", "k8s"} {
		provider := &ResolvedProvider{
			Command:    "codex",
			Args:       []string{"exec", "-c", "model_provider=host-gateway"},
			PromptMode: "arg",
			BoxArgs:    true,
		}
		ApplyRuntimeProviderShape(provider, runtimeName)
		if want := []string{"exec", "-c", "model_provider=host-gateway"}; !reflect.DeepEqual(provider.Args, want) {
			t.Errorf("runtime %q: Args = %v, want untouched %v", runtimeName, provider.Args, want)
		}
		if provider.PromptMode != "arg" {
			t.Errorf("runtime %q: PromptMode = %q, want the provider's own \"arg\"", runtimeName, provider.PromptMode)
		}
	}
}

func TestResolvedProviderCarriesBoxArgsThroughTheChain(t *testing.T) {
	// box_args is tri-state so a base layer can set it and a leaf inherit it,
	// the same shape supports_hooks uses. Absent must resolve false, or a
	// provider that never asked for its argv would start shipping it.
	set := true
	for name, spec := range map[string]*ProviderSpec{
		"declared": {Command: "codex", BoxArgs: &set},
		"absent":   {Command: "codex"},
	} {
		if got := specToResolved(name, spec).BoxArgs; got != (name == "declared") {
			t.Errorf("%s: BoxArgs = %v, want %v", name, got, name == "declared")
		}
	}
}
