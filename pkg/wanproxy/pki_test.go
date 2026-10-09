package wanproxy

import (
	"bytes"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsurePKIGenerateOnceAndReuse(t *testing.T) {
	dir := t.TempDir()

	first, err := EnsurePKI(dir)
	if err != nil {
		t.Fatalf("first EnsurePKI: %v", err)
	}
	if len(first.CAPEM) == 0 || len(first.CertPEM) == 0 || len(first.KeyPEM) == 0 {
		t.Fatalf("first PKI has empty material: %+v", first)
	}

	// All three files must be cached on disk.
	for _, name := range []string{caFileName, certFileName, keyFileName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected cached %s: %v", name, err)
		}
	}

	// Second call must reuse the cache byte-for-byte (generate once).
	second, err := EnsurePKI(dir)
	if err != nil {
		t.Fatalf("second EnsurePKI: %v", err)
	}
	if !bytes.Equal(first.CAPEM, second.CAPEM) ||
		!bytes.Equal(first.CertPEM, second.CertPEM) ||
		!bytes.Equal(first.KeyPEM, second.KeyPEM) {
		t.Fatalf("EnsurePKI regenerated material instead of reusing the cache")
	}
}

func TestEnsurePKIKeyFilePermissions(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsurePKI(dir); err != nil {
		t.Fatalf("EnsurePKI: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, keyFileName))
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("key file perm = %o, want 0600", perm)
	}
}

func TestPKILeafHasSANAndIsCASigned(t *testing.T) {
	dir := t.TempDir()
	pki, err := EnsurePKI(dir)
	if err != nil {
		t.Fatalf("EnsurePKI: %v", err)
	}

	caCert, err := parseCertPEM(pki.CAPEM)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}
	leaf, err := parseCertPEM(pki.CertPEM)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}

	// SAN "haify-proxy".
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != CertSAN {
		t.Fatalf("leaf DNSNames = %v, want [%q]", leaf.DNSNames, CertSAN)
	}

	// Signed by the CA.
	if err := leaf.CheckSignatureFrom(caCert); err != nil {
		t.Fatalf("leaf not signed by CA: %v", err)
	}
	if !caCert.IsCA {
		t.Fatalf("CA cert IsCA = false, want true")
	}

	// server + client EKU.
	if !hasEKU(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
		t.Fatalf("leaf missing ServerAuth EKU: %v", leaf.ExtKeyUsage)
	}
	if !hasEKU(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
		t.Fatalf("leaf missing ClientAuth EKU: %v", leaf.ExtKeyUsage)
	}

	// Chain verification through a roots pool (defense in depth).
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pki.CAPEM) {
		t.Fatalf("failed to add CA to pool")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
		DNSName:   CertSAN,
	}); err != nil {
		t.Fatalf("leaf chain verify failed: %v", err)
	}
}

func hasEKU(ekus []x509.ExtKeyUsage, want x509.ExtKeyUsage) bool {
	for _, e := range ekus {
		if e == want {
			return true
		}
	}
	return false
}
