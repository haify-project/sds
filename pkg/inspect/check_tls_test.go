package inspect

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func certPEM(t *testing.T, cn string, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn},
		NotBefore: notAfter.Add(-365 * 24 * time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestCertificateExpiry(t *testing.T) {
	in := cluster()
	api, err := ParseCertFile(certPEM(t, "api", t0.Add(20*24*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	in.TLS.APICert, in.TLS.APICertPath = api, "/etc/haify/tls/server.crt"
	in.Probes["n1"].TLSCert = certPEM(t, "n1", t0.Add(3*24*time.Hour))
	in.Probes["n2"].TLSCert = certPEM(t, "n2", t0.Add(-time.Hour))
	in.Probes["n3"].TLSCert = certPEM(t, "n3", t0.Add(200*24*time.Hour))

	checks := checkTLS(in)
	if c := only(t, checks, "tls.api_cert"); c.Status != StatusWarn {
		t.Errorf("got %+v", c)
	}
	repl := find(checks, "tls.replication_cert")
	if len(repl) != 2 {
		t.Fatalf("want n1 and n2:\n%s", dump(checks))
	}
	for _, c := range repl {
		if c.Status != StatusFail || c.Fix != "haify replication-tls setup" {
			t.Errorf("%+v", c)
		}
	}
}

func TestNoCertificatesIsAPass(t *testing.T) {
	allPass(t, checkTLS(cluster()))
}
