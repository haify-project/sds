package controller

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"os"
	"time"

	"google.golang.org/grpc/credentials"

	"github.com/liliang-cn/sds/pkg/config"
)

// tlsSetup is everything transport security needs once [tls] is enabled: the
// credentials the gRPC server listens with, and the credentials the in-process
// REST gateway dials back with.
//
// The two are not the same, and that asymmetry is the whole reason this type
// exists. See newTLSSetup for why the loopback hop cannot simply reuse a
// normal client configuration.
type tlsSetup struct {
	serverCreds   credentials.TransportCredentials
	loopbackCreds credentials.TransportCredentials
	// mutual reports that client certificates are required and verified.
	mutual bool
}

// loopbackIdentityValidity is how long the ephemeral client certificate the
// REST gateway uses is good for. It lives only in this process's memory and
// dies with it, so the number only has to outlast one controller run; a year
// keeps a long-lived controller from failing its own health checks.
const loopbackIdentityValidity = 365 * 24 * time.Hour

// newTLSSetup builds the transport credentials for a [tls] section. It returns
// (nil, nil) when TLS is disabled, which every caller must treat as "stay
// plaintext" rather than as an error.
//
// The configuration is validated at load time (config.TLSConfig.Validate), so
// a failure here means the certificate changed underneath a running
// controller, not that an operator typed something wrong.
func newTLSSetup(cfg config.TLSConfig) (*tlsSetup, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load cert_file %q with key_file %q: %w", cfg.CertFile, cfg.KeyFile, err)
	}
	if len(cert.Certificate) == 0 {
		return nil, fmt.Errorf("cert_file %q contains no certificate", cfg.CertFile)
	}

	serverConf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	// The REST gateway — and therefore the entire web UI — reaches the API by
	// dialling this same process at 127.0.0.1. That hop cannot be verified the
	// ordinary way: the operator's certificate is issued for the cluster's
	// service name or VIP, so its SANs will not cover the loopback literal,
	// and demanding that they do would make TLS unusable for everyone whose
	// certificate comes from a corporate or public CA. Skipping verification
	// outright is not acceptable either.
	//
	// Instead the hop pins the exact certificate that was just loaded from
	// disk: the peer is accepted only when it presents byte-for-byte the leaf
	// this process is serving. That is strictly stronger than CA verification
	// (no other certificate from the same CA is accepted), needs no extra
	// configuration, and is immune to whatever SANs the operator chose.
	loopbackConf := &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Disables the name/chain check only; VerifyPeerCertificate below is
		// the real check and it is unconditional.
		InsecureSkipVerify:    true, // #nosec G402 -- replaced by exact-leaf pinning
		VerifyPeerCertificate: pinnedLeafVerifier(cert.Certificate[0]),
	}

	if cfg.ClientCAFile != "" {
		pool, err := loadCertPool(cfg.ClientCAFile)
		if err != nil {
			return nil, err
		}

		// Mutual TLS would otherwise lock out the loopback hop along with
		// everyone else: the REST gateway has no client certificate, and
		// issuing it one from the operator's client CA is not something the
		// controller can do — it does not hold that CA's key.
		//
		// So the controller mints its own throwaway CA and a single client
		// leaf from it, in memory, at every start, and trusts that CA in
		// addition to the operator's. Nothing is written to disk and the key
		// never leaves this process, so unlike an address-based exemption for
		// 127.0.0.1 this does not hand every other local process a way around
		// client-certificate verification.
		loopbackCert, loopbackCA, err := newLoopbackIdentity()
		if err != nil {
			return nil, err
		}
		pool.AddCert(loopbackCA)

		serverConf.ClientCAs = pool
		serverConf.ClientAuth = tls.RequireAndVerifyClientCert
		loopbackConf.Certificates = []tls.Certificate{loopbackCert}
	}

	return &tlsSetup{
		serverCreds:   credentials.NewTLS(serverConf),
		loopbackCreds: credentials.NewTLS(loopbackConf),
		mutual:        cfg.ClientCAFile != "",
	}, nil
}

// pinnedLeafVerifier accepts a peer only when its leaf is byte-identical to
// want. Comparing DER rather than a fingerprint keeps the check trivially
// auditable and free of hash-agility questions.
func pinnedLeafVerifier(want []byte) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("loopback peer presented no certificate")
		}
		if !bytes.Equal(rawCerts[0], want) {
			return errors.New("loopback peer is not this controller: certificate does not match the configured tls.cert_file")
		}
		return nil
	}
}

// loadCertPool reads a PEM bundle into a fresh pool. An empty pool is an error
// rather than a permissive default: as ClientCAs it would reject every client,
// turning a typo in client_ca_file into a cluster-wide outage that looks like
// a certificate problem on the caller's side.
func loadCertPool(path string) (*x509.CertPool, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read client_ca_file %q: %w", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("client_ca_file %q contains no PEM certificate", path)
	}
	return pool, nil
}

// newLoopbackIdentity mints an in-memory CA and one client leaf signed by it,
// for the REST gateway's hop to this process's own gRPC server under mutual
// TLS. It returns the leaf (with its key) and the CA certificate to trust.
//
// Shaped after pkg/wanproxy.EnsurePKI — ed25519, self-signed CA, single leaf —
// minus the on-disk cache, which would only create a credential to steal.
func newLoopbackIdentity() (tls.Certificate, *x509.Certificate, error) {
	caPub, caPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("generate loopback CA key: %w", err)
	}
	now := time.Now().Add(-time.Minute) // small backdate for clock skew
	caSerial, err := randomCertSerial()
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          caSerial,
		Subject:               pkix.Name{CommonName: "sds-controller-loopback-ca"},
		NotBefore:             now,
		NotAfter:              now.Add(loopbackIdentityValidity),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPub, caPriv)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("create loopback CA cert: %w", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("parse loopback CA cert: %w", err)
	}

	leafPub, leafPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("generate loopback key: %w", err)
	}
	leafSerial, err := randomCertSerial()
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: leafSerial,
		Subject:      pkix.Name{CommonName: "sds-controller-rest-gateway"},
		NotBefore:    now,
		NotAfter:     now.Add(loopbackIdentityValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, leafPub, caPriv)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("create loopback cert: %w", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{leafDER},
		PrivateKey:  leafPriv,
	}, caCert, nil
}

// randomCertSerial returns a cryptographically-random 128-bit serial.
func randomCertSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	return serial, nil
}
