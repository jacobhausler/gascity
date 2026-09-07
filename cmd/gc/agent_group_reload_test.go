package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
)

func agentGroupCityTOML(t *testing.T, cityPath, groupBody string) string {
	t.Helper()
	rigPath := filepath.Join(cityPath, "rigs", "grouprig")
	if err := os.MkdirAll(rigPath, 0o755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}
	return `[workspace]
name = "test-city"

[beads]
provider = "file"

[session]
provider = "fake"

[[rigs]]
name = "grouprig"
path = "` + rigPath + `"

[[agent]]
name = "first"
dir = "grouprig"
lifecycle = "one_shot"
max_active_sessions = 4

[[agent]]
name = "second"
dir = "grouprig"
lifecycle = "one_shot"
max_active_sessions = 4
` + groupBody
}

// Reload validates agent groups, and a bad group is a WARNING rather than a
// hard failure — the same discipline as the rig check beside it, so a live city
// keeps running on the config it already has.
func TestCityRuntimeReloadValidatesAgentGroups(t *testing.T) {
	oldStrict := strictMode
	strictMode = false
	t.Cleanup(func() { strictMode = oldStrict })

	cityPath := t.TempDir()
	clearInheritedBeadsEnv(t)
	requireNoLeakedDoltAfterForPaths(t, cityPath)
	tomlPath := filepath.Join(cityPath, "city.toml")
	if err := os.WriteFile(tomlPath, []byte(agentGroupCityTOML(t, cityPath, "")), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}

	cfg, err := config.Load(osFS{}, tomlPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	sp := runtime.NewFake()
	var stderr bytes.Buffer
	cr := newTestCityRuntime(t, CityRuntimeParams{
		CityPath:  cityPath,
		CityName:  "test-city",
		TomlPath:  tomlPath,
		LogPrefix: "gc reload",
		Cfg:       cfg,
		SP:        sp,
		BuildFn: func(*config.City, runtime.Provider, beads.Store) DesiredStateResult {
			return DesiredStateResult{State: map[string]TemplateParams{}}
		},
		Dops:   newDrainOps(sp),
		Rec:    events.Discard,
		Stdout: io.Discard,
		Stderr: &stderr,
	})
	lastProviderName := "fake"

	// A group naming an agent that does not exist is reported, not adopted
	// silently.
	badGroup := `
[[agent_groups]]
name = "grouprig/heavy"
members = ["grouprig/first", "grouprig/ghost"]
`
	if err := os.WriteFile(tomlPath, []byte(agentGroupCityTOML(t, cityPath, badGroup)), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}
	reply := cr.reloadConfigTraced(context.Background(), &lastProviderName, cityPath, nil, reloadSourceManual)
	if !warningsContain(reply.Warnings, "is not a configured agent") {
		t.Fatalf("reply.Warnings = %v, want the invalid-member warning", reply.Warnings)
	}

	// A well-formed group reloads cleanly and lands in the live config.
	goodGroup := `
[[agent_groups]]
name = "grouprig/heavy"
members = ["grouprig/first", "grouprig/second"]
strategy = "ordered-failover"
`
	if err := os.WriteFile(tomlPath, []byte(agentGroupCityTOML(t, cityPath, goodGroup)), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}
	stderr.Reset()
	reply = cr.reloadConfigTraced(context.Background(), &lastProviderName, cityPath, nil, reloadSourceManual)
	for _, w := range reply.Warnings {
		if strings.Contains(w, "agent group") {
			t.Fatalf("valid group warned: %q", w)
		}
	}
	if g := config.FindAgentGroup(cr.cfg, "grouprig/heavy"); g == nil {
		t.Fatalf("reloaded config carries no agent group: %+v", cr.cfg.AgentGroups)
	} else if len(g.Members) != 2 {
		t.Fatalf("reloaded group members = %v, want two", g.Members)
	}
}
