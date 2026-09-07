package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/clientcontext"
)

// TestRemoteClientOptionsCarriesContextClientCertPair: a named context's mTLS
// pair reaches the transport options.
func TestRemoteClientOptionsCarriesContextClientCertPair(t *testing.T) {
	ctx := &clientcontext.Context{
		Name:           "westlands",
		URL:            "https://192.168.0.107:8443/api",
		City:           "westlands",
		CAFile:         "/secrets/relay-ca.pem",
		ClientCertFile: "/secrets/relay-client.pem",
		ClientKeyFile:  "/secrets/relay-client.key",
	}
	opts, err := remoteClientOptions(&remoteTarget{BaseURL: ctx.URL, CityName: "westlands", Ctx: ctx})
	if err != nil {
		t.Fatalf("remoteClientOptions: %v", err)
	}
	if opts.ClientCertFile != ctx.ClientCertFile || opts.ClientKeyFile != ctx.ClientKeyFile {
		t.Errorf("pair = %q/%q, want %q/%q", opts.ClientCertFile, opts.ClientKeyFile, ctx.ClientCertFile, ctx.ClientKeyFile)
	}
	if opts.CAFile != ctx.CAFile {
		t.Errorf("CAFile = %q, want %q", opts.CAFile, ctx.CAFile)
	}
}

// TestRemoteClientOptionsAdHocReadsTLSEnv: an ad-hoc --city-url target (Ctx nil)
// takes its TLS material from the environment — the tier that already supplies
// its bearer. This is the path an in-alloc agent uses, where the cert paths are
// only known at render time.
func TestRemoteClientOptionsAdHocReadsTLSEnv(t *testing.T) {
	t.Setenv("GC_CITY_CA", "/secrets/relay-ca.pem")
	t.Setenv("GC_CITY_CLIENT_CERT", "/secrets/relay-client.pem")
	t.Setenv("GC_CITY_CLIENT_KEY", "/secrets/relay-client.key")

	opts, err := remoteClientOptions(&remoteTarget{BaseURL: "https://192.168.0.107:8443/api", CityName: "westlands"})
	if err != nil {
		t.Fatalf("remoteClientOptions: %v", err)
	}
	if opts.CAFile != "/secrets/relay-ca.pem" {
		t.Errorf("CAFile = %q", opts.CAFile)
	}
	if opts.ClientCertFile != "/secrets/relay-client.pem" || opts.ClientKeyFile != "/secrets/relay-client.key" {
		t.Errorf("pair = %q/%q", opts.ClientCertFile, opts.ClientKeyFile)
	}
}

// TestRemoteClientOptionsContextIgnoresTLSEnv: a named context is authoritative.
// The ad-hoc environment tier must never silently swap a configured context's
// identity for whatever happens to be in the environment.
func TestRemoteClientOptionsContextIgnoresTLSEnv(t *testing.T) {
	t.Setenv("GC_CITY_CA", "/env/ca.pem")
	t.Setenv("GC_CITY_CLIENT_CERT", "/env/client.pem")
	t.Setenv("GC_CITY_CLIENT_KEY", "/env/client.key")

	ctx := &clientcontext.Context{Name: "westlands", URL: "https://city.example", City: "westlands"}
	opts, err := remoteClientOptions(&remoteTarget{BaseURL: ctx.URL, CityName: "westlands", Ctx: ctx})
	if err != nil {
		t.Fatalf("remoteClientOptions: %v", err)
	}
	if opts.CAFile != "" || opts.ClientCertFile != "" || opts.ClientKeyFile != "" {
		t.Errorf("context target picked up env TLS material: %q/%q/%q", opts.CAFile, opts.ClientCertFile, opts.ClientKeyFile)
	}
}
