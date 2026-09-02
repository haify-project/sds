package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTestServerCert generates a self-signed certificate and key into dir and
// returns their paths. Certificates are generated per test rather than checked
// in: a repository with a private key in it is a repository that eventually
// ships one.
func writeTestServerCert(t *testing.T, dir, name string) (certPath, keyPath string) {
	t.Helper()
	certPEM, keyPEM := generateTestCert(t, name, true)
	certPath = filepath.Join(dir, name+".crt")
	keyPath = filepath.Join(dir, name+".key")
	require.NoError(t, os.WriteFile(certPath, certPEM, 0o600))
	require.NoError(t, os.WriteFile(keyPath, keyPEM, 0o600))
	return certPath, keyPath
}

// writeTestClientCA writes a CA certificate usable as client_ca_file.
func writeTestClientCA(t *testing.T, dir string) string {
	t.Helper()
	certPEM, _ := generateTestCert(t, "test-client-ca", true)
	path := filepath.Join(dir, "client-ca.crt")
	require.NoError(t, os.WriteFile(path, certPEM, 0o600))
	return path
}

func generateTestCert(t *testing.T, cn string, isCA bool) (certPEM, keyPEM []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)
	now := time.Now().Add(-time.Minute)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             now,
		NotAfter:              now.Add(time.Hour),
		IsCA:                  isCA,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{cn, "localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

// Every case here used to pass validation and produce a controller that
// listened in plaintext while logging TLS as enabled. Rejecting them at load
// time — not at the first handshake — is the point of the section.
func TestTLSConfigValidate(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeTestServerCert(t, dir, "server")
	caFile := writeTestClientCA(t, dir)

	// A second, unrelated key pair: its key is a perfectly valid key that
	// simply does not belong to the first certificate. An existence check
	// waves this through and the failure surfaces at the first connection.
	otherCert, otherKey := writeTestServerCert(t, dir, "other")

	garbage := filepath.Join(dir, "garbage.pem")
	require.NoError(t, os.WriteFile(garbage, []byte("not a certificate\n"), 0o600))

	tests := []struct {
		name    string
		cfg     TLSConfig
		wantErr string
	}{
		{
			name: "disabled needs nothing",
			cfg:  TLSConfig{Enabled: false},
		},
		{
			name: "disabled ignores a certificate it will not use",
			cfg:  TLSConfig{Enabled: false, CertFile: certFile, KeyFile: keyFile},
		},
		{
			name:    "enabled without any certificate",
			cfg:     TLSConfig{Enabled: true},
			wantErr: "tls.cert_file and tls.key_file are required",
		},
		{
			name:    "enabled with a certificate but no key",
			cfg:     TLSConfig{Enabled: true, CertFile: certFile},
			wantErr: "tls.cert_file and tls.key_file are required",
		},
		{
			name:    "enabled with a key but no certificate",
			cfg:     TLSConfig{Enabled: true, KeyFile: keyFile},
			wantErr: "tls.cert_file and tls.key_file are required",
		},
		{
			name:    "certificate file does not exist",
			cfg:     TLSConfig{Enabled: true, CertFile: filepath.Join(dir, "absent.crt"), KeyFile: keyFile},
			wantErr: "cannot load cert_file",
		},
		{
			name:    "key file does not exist",
			cfg:     TLSConfig{Enabled: true, CertFile: certFile, KeyFile: filepath.Join(dir, "absent.key")},
			wantErr: "cannot load cert_file",
		},
		{
			name:    "key does not match the certificate",
			cfg:     TLSConfig{Enabled: true, CertFile: certFile, KeyFile: otherKey},
			wantErr: "cannot load cert_file",
		},
		{
			name:    "certificate is not PEM",
			cfg:     TLSConfig{Enabled: true, CertFile: garbage, KeyFile: keyFile},
			wantErr: "cannot load cert_file",
		},
		{
			name: "valid server-only TLS",
			cfg:  TLSConfig{Enabled: true, CertFile: certFile, KeyFile: keyFile},
		},
		{
			name: "valid mutual TLS",
			cfg:  TLSConfig{Enabled: true, CertFile: certFile, KeyFile: keyFile, ClientCAFile: caFile},
		},
		{
			name:    "client CA file does not exist",
			cfg:     TLSConfig{Enabled: true, CertFile: certFile, KeyFile: keyFile, ClientCAFile: filepath.Join(dir, "absent-ca.crt")},
			wantErr: "cannot read client_ca_file",
		},
		{
			name:    "client CA file holds no certificate",
			cfg:     TLSConfig{Enabled: true, CertFile: certFile, KeyFile: keyFile, ClientCAFile: garbage},
			wantErr: "contains no PEM certificate",
		},
		{
			name:    "legacy ca_cert is refused rather than ignored",
			cfg:     TLSConfig{Enabled: true, CertFile: certFile, KeyFile: keyFile, LegacyCACert: "/etc/sds/ca.crt"},
			wantErr: "no longer read",
		},
		{
			name:    "legacy client_cert is refused even with TLS off",
			cfg:     TLSConfig{Enabled: false, LegacyClientCert: otherCert},
			wantErr: "no longer read",
		},
		{
			name:    "legacy client_key is refused",
			cfg:     TLSConfig{Enabled: true, CertFile: certFile, KeyFile: keyFile, LegacyClientKey: otherKey},
			wantErr: "no longer read",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// Mutual TLS has no switch of its own — configuring a client CA is what turns
// it on. A CA that could be present without being enforced is the same class
// of bug as the section this replaces.
func TestTLSConfigMutualTLS(t *testing.T) {
	assert.False(t, TLSConfig{Enabled: true}.MutualTLS())
	assert.False(t, TLSConfig{Enabled: false, ClientCAFile: "/etc/sds/ca.crt"}.MutualTLS(),
		"a disabled section must not report mutual TLS")
	assert.True(t, TLSConfig{Enabled: true, ClientCAFile: "/etc/sds/ca.crt"}.MutualTLS())
}

// The whole config must fail to load, not just TLSConfig.Validate: the bug was
// that nobody called the validation at all.
func TestConfigValidateRunsTLSValidation(t *testing.T) {
	cfg := &Config{TLS: TLSConfig{Enabled: true}}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tls.cert_file")
}

// Save must not write the legacy keys back out; a round trip that reintroduced
// them would make the next start fail on a file the controller wrote itself.
func TestSaveOmitsLegacyTLSFields(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeTestServerCert(t, dir, "roundtrip")
	path := filepath.Join(dir, "saved.toml")

	cfg := &Config{
		Server: ServerConfig{ListenAddress: "127.0.0.1", Port: 3374},
		TLS:    TLSConfig{Enabled: true, CertFile: certFile, KeyFile: keyFile},
	}
	require.NoError(t, cfg.Save(path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "ca_cert")
	assert.NotContains(t, string(data), "client_cert")
	assert.NotContains(t, string(data), "client_key")
	assert.Contains(t, string(data), "cert_file")
}
