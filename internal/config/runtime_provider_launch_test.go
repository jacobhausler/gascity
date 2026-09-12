package config

import "testing"

// ResolveProviderForLaunch exists because the runtime shape was applied at
// exactly ONE of twelve production call sites that compose a launch or resume
// command, and applied too late even there — resolveTemplate built the command
// string from resolved.Args ~280 lines before nilling it.
//
// The live cost, measured 2026-09-12: a lane declaring runtime_provider="nomad"
// still got its provider's args_append in argv, so every box ran `codex exec`
// with no prompt and the agent exited 1 in the same second it started, for
// days. Gate 3 ("any lane onboards by naming its runtime") was not finished;
// it was finished on one path.
//
// A wrapper does not make the mistake impossible on its own. What it does is
// give every launch path one correct call to make, and give the ratchet guard
// in cmd/gc something to count against.
func TestResolveProviderForLaunchAppliesTheShape(t *testing.T) {
	agent := &Agent{Name: "runner", Provider: "p", RuntimeProvider: NomadRuntimeProvider}
	providers := map[string]ProviderSpec{
		"p": {Command: "codex", Args: []string{"exec", "-c", "model_provider=x"}, PromptMode: "arg"},
	}
	ws := &Workspace{Provider: "p"}
	look := func(string) (string, error) { return "/bin/echo", nil }

	// The bare call is the unshaped truth, and MUST stay that way: gc doctor,
	// gc config explain and the dashboard's effective-options view all exist to
	// show what the provider actually declares.
	raw, err := ResolveProvider(agent, ws, providers, look)
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if len(raw.Args) == 0 {
		t.Fatal("the bare resolve dropped Args; diagnostics depend on it showing the provider as declared")
	}

	shaped, err := ResolveProviderForLaunch(agent, ws, providers, look, NomadRuntimeProvider)
	if err != nil {
		t.Fatalf("ResolveProviderForLaunch: %v", err)
	}
	if len(shaped.Args) != 0 {
		t.Errorf("Args survived the launch resolve: %v — the runtime owns argv on this runtime", shaped.Args)
	}
	if shaped.PromptMode != "none" {
		t.Errorf("PromptMode = %q, want none: the runtime owns prompt delivery", shaped.PromptMode)
	}
	if shaped.SupportsHooks {
		t.Error("SupportsHooks survived the launch resolve")
	}
}

// A lane on no special runtime must be byte-identical through either call, or
// this wrapper is a behavior change for every existing deployment rather than
// a fix for one runtime.
func TestResolveProviderForLaunchIsANoOpOffNomad(t *testing.T) {
	agent := &Agent{Name: "runner", Provider: "p"}
	providers := map[string]ProviderSpec{
		"p": {Command: "codex", Args: []string{"exec"}, PromptMode: "arg"},
	}
	ws := &Workspace{Provider: "p"}
	look := func(string) (string, error) { return "/bin/echo", nil }

	raw, err := ResolveProvider(agent, ws, providers, look)
	if err != nil {
		t.Fatal(err)
	}
	for _, rt := range []string{"", "local", "exec:whatever"} {
		got, err := ResolveProviderForLaunch(agent, ws, providers, look, rt)
		if err != nil {
			t.Fatalf("runtime %q: %v", rt, err)
		}
		if len(got.Args) != len(raw.Args) || got.PromptMode != raw.PromptMode || got.SupportsHooks != raw.SupportsHooks {
			t.Errorf("runtime %q changed a non-nomad lane: args=%v promptMode=%q hooks=%v",
				rt, got.Args, got.PromptMode, got.SupportsHooks)
		}
	}
}
