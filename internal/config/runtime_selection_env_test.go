package config

import "testing"

// cr-8eagyh. Some environment belongs to WHERE a session runs, not to what the
// agent is. Declaring it once per runtime is what lets a lane onboard by naming
// its runtime instead of inheriting a wrapper provider that restates the same
// keys — and getting that wrapper wrong produced a box with no agent in it and
// no error anywhere.
func TestRuntimeSelectionEnv(t *testing.T) {
	cfg := &City{}
	cfg.Session.RuntimeEnv = map[string]map[string]string{
		"nomad": {"GC_CITY_URL": "https://192.168.0.107:8443/api", "GC_CITY_NAME": "westlands"},
	}

	t.Run("declared runtime gets its env", func(t *testing.T) {
		got := RuntimeSelectionEnv(cfg, "nomad")
		if got["GC_CITY_URL"] != "https://192.168.0.107:8443/api" || got["GC_CITY_NAME"] != "westlands" {
			t.Fatalf("nomad env = %v, want the declared city endpoint", got)
		}
	})

	t.Run("a different runtime gets nothing", func(t *testing.T) {
		if got := RuntimeSelectionEnv(cfg, "local"); got != nil {
			t.Fatalf("local env = %v, want nil — this must never leak onto local seats, which would push them onto the remote path", got)
		}
	})

	t.Run("empty selection gets nothing", func(t *testing.T) {
		if got := RuntimeSelectionEnv(cfg, ""); got != nil {
			t.Fatalf("empty selection = %v, want nil", got)
		}
	})

	t.Run("nil city does not panic", func(t *testing.T) {
		if got := RuntimeSelectionEnv(nil, "nomad"); got != nil {
			t.Fatalf("nil city = %v, want nil", got)
		}
	})

	t.Run("returned map is detached from the config", func(t *testing.T) {
		got := RuntimeSelectionEnv(cfg, "nomad")
		got["GC_CITY_URL"] = "mutated"
		if cfg.Session.RuntimeEnv["nomad"]["GC_CITY_URL"] == "mutated" {
			t.Fatal("a caller mutating the returned env must not rewrite the city config")
		}
	})

	t.Run("no declaration at all", func(t *testing.T) {
		if got := RuntimeSelectionEnv(&City{}, "nomad"); got != nil {
			t.Fatalf("undeclared = %v, want nil", got)
		}
	})
}
