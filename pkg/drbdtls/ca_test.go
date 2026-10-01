package drbdtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func csrFor(t *testing.T, cn string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func TestEnsureCreatesOnceAndReloads(t *testing.T) {
	dir := t.TempDir()
	a, err := Ensure(dir)
	require.NoError(t, err)
	b, err := Ensure(dir)
	require.NoError(t, err)
	assert.Equal(t, a.Fingerprint(), b.Fingerprint(), "a second call must not mint a new CA")

	st, err := os.Stat(filepath.Join(dir, caKeyFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
}

// Half a CA is not regenerated silently: a fresh one would orphan every node
// certificate already issued.
func TestEnsureRefusesAHalfCA(t *testing.T) {
	dir := t.TempDir()
	_, err := Ensure(dir)
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(dir, caKeyFile)))
	_, err = Ensure(dir)
	require.Error(t, err)
}

func TestSignIssuesACertificateTheCAAccepts(t *testing.T) {
	ca, err := Ensure(t.TempDir())
	require.NoError(t, err)
	cert, err := ca.Sign(csrFor(t, "n1"), "n1", []string{"10.0.0.1"})
	require.NoError(t, err)

	expires, err := ca.Check(cert, "n1", time.Now())
	require.NoError(t, err)
	assert.True(t, expires.After(time.Now().Add(4*365*24*time.Hour)))

	parsed, err := parseCert(cert)
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1", parsed.IPAddresses[0].String())
	assert.ElementsMatch(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, parsed.ExtKeyUsage)
}

func TestSignRefusesARequestForAnotherNode(t *testing.T) {
	ca, err := Ensure(t.TempDir())
	require.NoError(t, err)
	_, err = ca.Sign(csrFor(t, "n2"), "n1", nil)
	require.Error(t, err)
}

func TestCheckRejectsAnotherCAsCertificate(t *testing.T) {
	ours, err := Ensure(t.TempDir())
	require.NoError(t, err)
	theirs, err := Ensure(t.TempDir())
	require.NoError(t, err)
	cert, err := theirs.Sign(csrFor(t, "n1"), "n1", nil)
	require.NoError(t, err)
	_, err = ours.Check(cert, "n1", time.Now())
	require.Error(t, err)
}
