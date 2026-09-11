package buildimage

import (
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

func TestRenderRuntimeCodexConfigUsesNomadProviderAndUpstream(t *testing.T) {
	base := "builtin:codex"
	const authCanary = "DO-NOT-BAKE-THIS-CREDENTIAL"
	cfg := &config.City{
		Upstreams: map[string]config.UpstreamSpec{
			"firehose": {
				BaseURL:   "http://gateway.example/v1",
				APIKey:    "$OPENAI_API_KEY",
				APIKeyEnv: "OPENAI_API_KEY",
				Env:       map[string]string{"OPENAI_API_KEY": authCanary},
			},
		},
		Providers: map[string]config.ProviderSpec{
			"qwen": {
				Base: &base,
				ArgsAppend: []string{
					"exec", "--skip-git-repo-check",
					"-c", "model_provider=haus_qwen",
					"-c", "model=qwen-model",
					"-c", "model_reasoning_effort=medium",
					"-c", "model_auto_compact_token_limit=131072",
				},
			},
		},
		Agents: []config.Agent{{
			Name:            "worker",
			Provider:        "qwen",
			RuntimeProvider: "nomad",
			Upstream:        "firehose",
		}},
	}

	got, err := RenderRuntimeCodexConfig(cfg)
	if err != nil {
		t.Fatalf("RenderRuntimeCodexConfig: %v", err)
	}
	text := string(got)
	for _, want := range []string{
		`model = "qwen-model"`,
		`model_provider = "haus_qwen"`,
		`model_reasoning_effort = "medium"`,
		"model_auto_compact_token_limit = 131072",
		"[model_providers.haus_qwen]",
		`base_url = "http://gateway.example/v1"`,
		`env_key = "OPENAI_API_KEY"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered config missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, authCanary) || strings.Contains(text, "OPENAI_API_KEY =") {
		t.Fatalf("rendered config contains credential material:\n%s", text)
	}
}

func TestRenderRuntimeCodexConfigIgnoresNonNomadAgents(t *testing.T) {
	base := "builtin:codex"
	cfg := &config.City{
		Upstreams: map[string]config.UpstreamSpec{
			"local": {BaseURL: "http://local.example/v1"},
		},
		Providers: map[string]config.ProviderSpec{
			"codex-local": {
				Base:       &base,
				ArgsAppend: []string{"-c", "model_provider=local"},
			},
		},
		Agents: []config.Agent{{
			Name:            "worker",
			Provider:        "codex-local",
			RuntimeProvider: "subprocess",
			Upstream:        "local",
		}},
	}

	got, err := RenderRuntimeCodexConfig(cfg)
	if err != nil {
		t.Fatalf("RenderRuntimeCodexConfig: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("non-Nomad config = %q, want empty", got)
	}
}
