package main

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

// The box half of cr-jsxjcr, asserted where it was actually broken: the
// RENDERED COMMAND, not resolved.Args. resolveTemplate composes
// `command = resolved.CommandString()`, which folds Args into a plain string,
// and the runtime shape runs before it (template_resolve.go:200) — so the only
// artifact that reaches a Nomad box is this string, and a test on the pointer
// field the shape mutates would pass whether or not anything reached the box.
//
// TestResolveTemplateAppliesNomadRuntimeShapeBeforeComposingCommand pins the
// shipped behavior: a lane naming runtime_provider = "nomad" gets the bare
// interactive client, primed by the adapter. This one pins the exception: a
// provider that declares `box_args = true` is an exec-mode agent, and dropping
// its argv does not make the config honest — it gives every box the TUI, which
// answers one turn and then sits at its prompt with nothing to wake it.
func TestResolveTemplateKeepsBoxArgsProviderArgvInRenderedCommand(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")

	base := "builtin:codex"
	yes := true
	providers := map[string]config.ProviderSpec{
		"nomad-exec": {
			Base: &base,
			ArgsAppend: []string{
				"exec",
				"--skip-git-repo-check",
				"-c", "model_provider=x",
				"-c", "model=qwen3.8",
			},
			BoxArgs: &yes,
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
		Provider:        "nomad-exec",
		RuntimeProvider: "nomad",
	}

	tp, err := resolveTemplate(params, agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}
	// Vacuity guard: the provider has to have resolved, or the token assertions
	// below pass on an empty command.
	if tp.ResolvedProvider == nil || tp.ResolvedProvider.Command == "" {
		t.Fatal("provider did not resolve; the token assertions below would be vacuous")
	}

	for _, token := range []string{"exec", "--skip-git-repo-check", "model=qwen3.8"} {
		if !strings.Contains(tp.Command, token) {
			t.Errorf("rendered command dropped %q, so box_args did not reach argv: %q", token, tp.Command)
		}
	}
	// The host-only setting stays behind the boundary: model_provider names a
	// key in the host's catalog, while the box authenticates against the
	// provider stanza buildimage bakes into its own config.toml.
	if strings.Contains(tp.Command, "model_provider=x") {
		t.Errorf("rendered command carries the host's model_provider, which no config inside the box defines: %q", tp.Command)
	}
	// Prompt delivery is still the runtime's: the adapter primes from the nudge
	// on the start wire, and gc must not also append a prompt to a command the
	// adapter may run more than once.
	if tp.ResolvedProvider.PromptMode != "none" {
		t.Errorf("prompt_mode = %q, want \"none\"", tp.ResolvedProvider.PromptMode)
	}
}
