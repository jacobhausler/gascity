package buildimage

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
)

const (
	codexDefaultWireAPI      = "responses"
	codexStreamIdleTimeoutMS = 1800000
)

type runtimeCodexProvider struct {
	modelProvider string
	model         string
	effort        string
	compact       string
	baseURL       string
	envKey        string
	wireAPI       string
}

// RenderRuntimeCodexConfig renders the secret-free Codex config needed by
// providers whose agents are routed to Nomad. Provider command settings supply
// the model/provider identity; the selected city upstream supplies the serving
// URL and only the credential environment-variable name is retained.
//
// The result is intentionally empty when no agent selects Nomad. This keeps
// local and legacy image builds unchanged.
func RenderRuntimeCodexConfig(city *config.City) ([]byte, error) {
	if city == nil {
		return nil, nil
	}

	providers := make(map[string]runtimeCodexProvider)
	for i := range city.Agents {
		agent := &city.Agents[i]
		runtimeProvider, _ := config.ResolveAgentRuntimeProvider(city, agent)
		if runtimeProvider != config.NomadRuntimeProvider {
			continue
		}

		providerName := strings.TrimSpace(agent.Provider)
		if providerName == "" {
			providerName = strings.TrimSpace(city.Workspace.Provider)
		}
		if providerName == "" {
			continue
		}
		resolved, err := runtimeResolvedProvider(city, providerName)
		if err != nil {
			return nil, fmt.Errorf("agent %q: resolving provider %q: %w", agent.QualifiedName(), providerName, err)
		}
		if resolved.BuiltinAncestor != "codex" && config.BuiltinFamily(providerName, city.Providers) != "codex" {
			continue
		}

		upstreamName := strings.TrimSpace(agent.Upstream)
		if upstreamName == "" {
			continue
		}
		upstream, ok := city.Upstreams[upstreamName]
		if !ok {
			return nil, fmt.Errorf("agent %q selects upstream %q which is not declared in [upstreams]", agent.QualifiedName(), upstreamName)
		}

		settings := codexCommandSettings(resolved.Args)
		// Schema-managed Codex flags are normalized out of Args during provider
		// resolution. EffectiveDefaults retains the same authored values, so use
		// it as the source for model/effort when the normalized argv no longer
		// carries those -c settings.
		if settings["model"] == "" {
			settings["model"] = resolved.EffectiveDefaults["model"]
		}
		if settings["model_reasoning_effort"] == "" {
			settings["model_reasoning_effort"] = resolved.EffectiveDefaults["effort"]
		}
		modelProvider := strings.TrimSpace(settings["model_provider"])
		if modelProvider == "" {
			modelProvider = providerName
		}
		if !validCodexToken(modelProvider) {
			return nil, fmt.Errorf("agent %q provider %q has invalid model_provider %q", agent.QualifiedName(), providerName, modelProvider)
		}

		baseURL := strings.TrimSpace(upstream.BaseURL)
		if baseURL == "" && resolved.UpstreamEnv.BaseURL != "" {
			baseURL = strings.TrimSpace(upstream.Env[resolved.UpstreamEnv.BaseURL])
		}
		if baseURL == "" && resolved.UpstreamEnv.BaseURL != "" {
			baseURL = strings.TrimSpace(resolved.Env[resolved.UpstreamEnv.BaseURL])
		}
		if !validCodexBaseURL(baseURL) {
			return nil, fmt.Errorf("agent %q upstream %q has no literal http(s) base_url for the baked Codex provider", agent.QualifiedName(), upstreamName)
		}

		envKey := strings.TrimSpace(upstream.APIKeyEnv)
		if envKey == "" {
			envKey = strings.TrimSpace(resolved.UpstreamEnv.APIKey)
		}
		if envKey != "" && !validCodexToken(envKey) {
			return nil, fmt.Errorf("agent %q upstream %q has invalid api key environment name %q", agent.QualifiedName(), upstreamName, envKey)
		}

		entry := runtimeCodexProvider{
			modelProvider: modelProvider,
			model:         strings.TrimSpace(settings["model"]),
			effort:        strings.TrimSpace(settings["model_reasoning_effort"]),
			compact:       strings.TrimSpace(settings["model_auto_compact_token_limit"]),
			baseURL:       baseURL,
			envKey:        envKey,
			wireAPI:       strings.TrimSpace(settings["wire_api"]),
		}
		if entry.wireAPI == "" {
			entry.wireAPI = codexDefaultWireAPI
		}
		if !validCodexToken(entry.wireAPI) {
			return nil, fmt.Errorf("agent %q provider %q has invalid wire_api %q", agent.QualifiedName(), providerName, entry.wireAPI)
		}
		if entry.compact != "" && !allDigits(entry.compact) {
			entry.compact = ""
		}

		if prior, exists := providers[modelProvider]; exists && prior != entry {
			return nil, fmt.Errorf("nomad Codex provider %q is configured inconsistently", modelProvider)
		}
		providers[modelProvider] = entry
	}

	if len(providers) == 0 {
		return nil, nil
	}

	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	first := providers[names[0]]
	var out strings.Builder
	writeCodexSetting(&out, "model", first.model)
	writeCodexSetting(&out, "model_provider", first.modelProvider)
	writeCodexSetting(&out, "model_reasoning_effort", first.effort)
	if first.compact != "" {
		fmt.Fprintf(&out, "model_auto_compact_token_limit = %s\n", first.compact)
	}
	out.WriteString("approval_policy = \"never\"\n")
	out.WriteString("sandbox_mode = \"danger-full-access\"\n\n")
	for i, name := range names {
		if i > 0 {
			out.WriteByte('\n')
		}
		entry := providers[name]
		fmt.Fprintf(&out, "[model_providers.%s]\n", name)
		fmt.Fprintf(&out, "name = %s\n", tomlQuote(name))
		fmt.Fprintf(&out, "base_url = %s\n", tomlQuote(entry.baseURL))
		fmt.Fprintf(&out, "wire_api = %s\n", tomlQuote(entry.wireAPI))
		if entry.envKey != "" {
			fmt.Fprintf(&out, "env_key = %s\n", tomlQuote(entry.envKey))
		}
		out.WriteString("requires_openai_auth = false\n")
		out.WriteString("request_max_retries = 0\n")
		out.WriteString("stream_max_retries = 0\n")
		fmt.Fprintf(&out, "stream_idle_timeout_ms = %d\n", codexStreamIdleTimeoutMS)
	}
	return []byte(out.String()), nil
}

func runtimeResolvedProvider(city *config.City, name string) (*config.ResolvedProvider, error) {
	if resolved, ok := config.ResolvedProviderCached(city, name); ok {
		return &resolved, nil
	}
	spec, ok := city.Providers[name]
	if !ok {
		return nil, fmt.Errorf("provider is not in the explicit provider catalog")
	}
	resolved, err := config.ResolveProviderChain(name, spec, city.Providers)
	if err != nil {
		return nil, err
	}
	if resolved.BuiltinAncestor == "" {
		resolved.BuiltinAncestor = config.BuiltinFamily(name, city.Providers)
	}
	return &resolved, nil
}

func codexCommandSettings(args []string) map[string]string {
	settings := make(map[string]string)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-c" && i+1 < len(args):
			i++
			addCodexSetting(settings, args[i])
		case strings.HasPrefix(arg, "-c") && len(arg) > 2:
			addCodexSetting(settings, arg[2:])
		case arg == "--model" && i+1 < len(args):
			i++
			settings["model"] = strings.TrimSpace(args[i])
		}
	}
	return settings
}

func addCodexSetting(settings map[string]string, raw string) {
	key, value, ok := strings.Cut(raw, "=")
	if !ok {
		return
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" {
		return
	}
	if unquoted, err := strconv.Unquote(value); err == nil {
		value = unquoted
	}
	settings[key] = value
}

func writeCodexSetting(out *strings.Builder, key, value string) {
	if value != "" {
		fmt.Fprintf(out, "%s = %s\n", key, tomlQuote(value))
	}
}

func tomlQuote(value string) string {
	return strconv.Quote(value)
}

func validCodexToken(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '.' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func validCodexBaseURL(value string) bool {
	return (strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")) &&
		!strings.ContainsAny(value, " \t\r\n\"'\\")
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
