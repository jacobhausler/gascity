package clientcontext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestClientCertPairRoundTripsThroughContextsFile: the mTLS pair survives
// Save/Load under its documented TOML keys.
func TestClientCertPairRoundTripsThroughContextsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contexts.toml")
	want := &File{Contexts: []Context{{
		Name:           "westlands",
		URL:            "https://192.168.0.107:8443/api",
		City:           "westlands",
		CAFile:         "/secrets/relay-ca.pem",
		ClientCertFile: "/secrets/relay-client.pem",
		ClientKeyFile:  "/secrets/relay-client.key",
	}}}
	if err := want.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"client_cert_file", "client_key_file"} {
		if !strings.Contains(string(data), key) {
			t.Errorf("contexts.toml omits %s: %s", key, data)
		}
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	c := got.Contexts[0]
	if c.ClientCertFile != want.Contexts[0].ClientCertFile || c.ClientKeyFile != want.Contexts[0].ClientKeyFile {
		t.Errorf("pair = %q/%q, want %q/%q", c.ClientCertFile, c.ClientKeyFile,
			want.Contexts[0].ClientCertFile, want.Contexts[0].ClientKeyFile)
	}
}

// TestValidateRejectsHalfClientCertPair: half a pair is a config error, so an
// operator learns at `gc context add` rather than at handshake time.
func TestValidateRejectsHalfClientCertPair(t *testing.T) {
	for name, c := range map[string]Context{
		"cert without key": {Name: "a", URL: "https://x.example", ClientCertFile: "/c.pem"},
		"key without cert": {Name: "a", URL: "https://x.example", ClientKeyFile: "/c.key"},
	} {
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "must be set together") {
			t.Errorf("%s: Validate() = %v, want a must-be-set-together error", name, err)
		}
	}
}

// TestValidateAcceptsCompleteClientCertPair guards the happy path against an
// over-eager rejection.
func TestValidateAcceptsCompleteClientCertPair(t *testing.T) {
	c := Context{Name: "a", URL: "https://x.example", ClientCertFile: "/c.pem", ClientKeyFile: "/c.key"}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
