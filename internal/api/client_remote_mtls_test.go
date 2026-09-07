package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeMTLSFixture mints a single-CA fixture: a CA, a server leaf for
// 127.0.0.1, and a client leaf. It returns the directory plus the paths a
// RemoteOptions would carry, and the *tls.Config a require_and_verify server
// would use.
func writeMTLSFixture(t *testing.T) (caFile, certFile, keyFile string, serverTLS *tls.Config) {
	t.Helper()
	dir := t.TempDir()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("ca cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}

	leaf := func(cn string, eku x509.ExtKeyUsage, ips []net.IP) (certPEM, keyPEM []byte) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("%s key: %v", cn, err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{eku},
			IPAddresses:  ips,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatalf("%s cert: %v", cn, err)
		}
		keyDER, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatalf("%s marshal: %v", cn, err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	}

	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}

	caFile = write("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	srvCertPEM, srvKeyPEM := leaf("server", x509.ExtKeyUsageServerAuth, []net.IP{net.ParseIP("127.0.0.1")})
	cliCertPEM, cliKeyPEM := leaf("client", x509.ExtKeyUsageClientAuth, nil)
	certFile = write("client.pem", cliCertPEM)
	keyFile = write("client-key.pem", cliKeyPEM)

	srvPair, err := tls.X509KeyPair(srvCertPEM, srvKeyPEM)
	if err != nil {
		t.Fatalf("server pair: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	serverTLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{srvPair},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}
	return caFile, certFile, keyFile, serverTLS
}

// TestRemoteTLSConfigLoadsClientCertificate is the unit contract: the pair
// named by RemoteOptions lands in tls.Config.Certificates.
func TestRemoteTLSConfigLoadsClientCertificate(t *testing.T) {
	caFile, certFile, keyFile, _ := writeMTLSFixture(t)
	cfg, err := remoteTLSConfig(RemoteOptions{CAFile: caFile, ClientCertFile: certFile, ClientKeyFile: keyFile})
	if err != nil {
		t.Fatalf("remoteTLSConfig: %v", err)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("Certificates len = %d, want 1", len(cfg.Certificates))
	}
	if cfg.RootCAs == nil {
		t.Error("RootCAs not populated from CAFile")
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want TLS1.2", cfg.MinVersion)
	}
}

// TestRemoteTLSConfigRejectsHalfPair: a cert without its key (or vice versa) is
// a construction error, not a silently unauthenticated connection.
func TestRemoteTLSConfigRejectsHalfPair(t *testing.T) {
	_, certFile, keyFile, _ := writeMTLSFixture(t)
	for name, opts := range map[string]RemoteOptions{
		"cert without key": {ClientCertFile: certFile},
		"key without cert": {ClientKeyFile: keyFile},
	} {
		if _, err := remoteTLSConfig(opts); err == nil || !strings.Contains(err.Error(), "must be set together") {
			t.Errorf("%s: err = %v, want a must-be-set-together error", name, err)
		}
	}
}

// TestRemoteTLSConfigRejectsUnreadablePair surfaces a bad path at construction.
func TestRemoteTLSConfigRejectsUnreadablePair(t *testing.T) {
	dir := t.TempDir()
	_, err := remoteTLSConfig(RemoteOptions{
		ClientCertFile: filepath.Join(dir, "nope.pem"),
		ClientKeyFile:  filepath.Join(dir, "nope-key.pem"),
	})
	if err == nil || !strings.Contains(err.Error(), "loading client certificate") {
		t.Fatalf("err = %v, want a loading client certificate error", err)
	}
}

// TestRemoteClientReachesRequireAndVerifyServer is the behavior the DF-03b
// relay needs end to end: against a server configured exactly like the
// control-plane relay (RequireAndVerifyClientCert), a client WITH the pair
// completes the handshake and a client WITHOUT it is refused.
func TestRemoteClientReachesRequireAndVerifyServer(t *testing.T) {
	caFile, certFile, keyFile, serverTLS := writeMTLSFixture(t)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	srv.TLS = serverTLS
	srv.StartTLS()
	defer srv.Close()

	get := func(opts RemoteOptions) error {
		cfg, err := remoteTLSConfig(opts)
		if err != nil {
			return err
		}
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}, Timeout: 5 * time.Second}
		resp, err := c.Get(srv.URL)
		if err != nil {
			return err
		}
		defer resp.Body.Close() //nolint:errcheck
		return nil
	}

	if err := get(RemoteOptions{CAFile: caFile, ClientCertFile: certFile, ClientKeyFile: keyFile}); err != nil {
		t.Fatalf("with client cert: %v", err)
	}
	if err := get(RemoteOptions{CAFile: caFile}); err == nil {
		t.Fatal("without a client cert the handshake must fail")
	}
}
