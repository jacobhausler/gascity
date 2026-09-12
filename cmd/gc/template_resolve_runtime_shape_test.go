package main

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

// THE DEFECT, measured on a live cluster 2026-09-12 and dark for days.
//
// config.ApplyRuntimeProviderShape imposes the facts the RUNTIME owns onto a
// resolved provider — for the nomad runtime, Args = nil, PromptMode = "none",
// SupportsHooks = false — so that a lane can move to Nomad by naming its
// runtime instead of hand-copying a wrapper provider that restates them.
//
// Its only production call site ran AFTER the launch command had already been
// composed: resolveTemplate builds `command = resolved.CommandString()`, which
// folds resolved.Args (carrying the provider's args_append) into a plain
// string, ~280 lines before the shape nils resolved.Args. Mutating a field
// after its value has been copied into a string cannot change the string, so
// the shape was a no-op for the one thing that reaches the box: argv.
//
// What it cost: a provider whose args_append begins with "exec" gave every box
// `codex exec` with no prompt — one-shot mode with nothing to run — and the
// agent exited 1 in the same second it started, forever. The box's own
// captured output was "No prompt provided. Either specify one as an argument
// or pipe the prompt into stdin." With the shape applied in time, argv is bare
// `codex`: the interactive client the Nomad runtime primes by typing into the
// pane, which is what PromptMode="none" means.
//
// Asserted on the RENDERED COMMAND, not on resolved.Args. Args is a pointer
// field mutated in place, so it reads as nil at the end of resolveTemplate
// whatever the ordering — a test that checked it would have passed throughout
// the outage. The command string is the artifact that was wrong.
func TestResolveTemplateAppliesNomadRuntimeShapeBeforeComposingCommand(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")

	// args_append reaches ResolvedProvider.Args only through the
	// inheritance-chain merge, so the spec needs a Base. Basing on the codex
	// builtin mirrors the production shape exactly: a provider layering
	// args_append over builtin:codex.
	base := "builtin:codex"
	providers := map[string]config.ProviderSpec{
		"nomad-codex": {
			Base:       &base,
			ArgsAppend: []string{"exec", "-c", "model_provider=x"},
		},
	}

	params := &agentBuildParams{
		cityName:   "city",
		cityPath:   cityPath,
		workspace:  &config.Workspace{Provider: "test"},
		providers:  providers,
		lookPath:   func(string) (string, error) { return "/bin/echo", nil },
		fs:         fsys.OSFS{},
		beaconTime: time.Unix(0, 0),
		beadNames:  make(map[string]string),
		stderr:     io.Discard,
	}

	agent := &config.Agent{
		Name:            "runner",
		Provider:        "nomad-codex",
		RuntimeProvider: "nomad",
	}

	tp, err := resolveTemplate(params, agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}

	for _, token := range []string{"exec", "model_provider=x"} {
		if strings.Contains(tp.Command, token) {
			t.Errorf("rendered command still carries the provider's args_append token %q, so the runtime shape did not reach argv: %q", token, tp.Command)
		}
	}

	// And the lane must still be on the same provider: this is a shape, not a
	// substitution. A test that passed because the provider failed to resolve
	// would prove nothing.
	if tp.ResolvedProvider == nil || tp.ResolvedProvider.Command == "" {
		t.Fatalf("provider did not resolve; the assertion above would be vacuous")
	}
}
