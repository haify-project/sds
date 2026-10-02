package client

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strconv"

	"google.golang.org/grpc/credentials"
)

// TLSOptions describes how a client verifies the controller, and what it
// presents when the controller requires a client certificate.
//
// These fields are named from the CLIENT's point of view, which is the reason
// the same three names were wrong in controller.toml: there they described the
// server, which needs its own certificate and its own key. Here they are
// exactly right — the CA verifies the peer, the certificate and key are ours.
type TLSOptions struct {
	// Enabled turns on transport TLS. It is implied by any of the fields
	// below, so `--tls-ca ...` alone is enough; the flag exists for a
	// controller whose certificate chains to the system trust store, where
	// there is nothing else to configure.
	Enabled bool
	// CACert is a PEM bundle that must have signed the controller's
	// certificate. Empty means the host's system trust store.
	CACert string
	// ClientCert and ClientKey are presented when the controller sets
	// tls.client_ca_file (mutual TLS). Both or neither.
	ClientCert string
	ClientKey  string
	// ServerName overrides the name checked against the controller's
	// certificate, for the case where the CLI reaches it by an address the
	// certificate does not name — a VIP, an SSH tunnel, a port-forward.
	ServerName string
	// Insecure skips verification entirely. It exists because operators will
	// otherwise reach for `--tls-ca /dev/null` and worse, but it turns TLS
	// into encryption without authentication: the connection is still
	// trivially machine-in-the-middled, bearer token and all.
	Insecure bool
}

// Active reports whether the connection should use TLS at all. Any TLS field
// implies it, so a caller cannot ask for a CA and silently get plaintext.
func (o TLSOptions) Active() bool {
	return o.Enabled || o.CACert != "" || o.ClientCert != "" || o.ClientKey != "" || o.Insecure
}

// Credentials builds the transport credentials for these options.
func (o TLSOptions) Credentials() (credentials.TransportCredentials, error) {
	conf := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         o.ServerName,
		InsecureSkipVerify: o.Insecure, // #nosec G402 -- opt-in, see TLSOptions.Insecure
	}

	if o.CACert != "" {
		pemBytes, err := os.ReadFile(o.CACert)
		if err != nil {
			return nil, fmt.Errorf("read TLS CA %q: %w", o.CACert, err)
		}
		pool := x509.NewCertPool()
		// An empty pool would fall through to "verify against nothing", which
		// fails every handshake with an error about the server's certificate —
		// pointing the operator at the wrong end of the connection.
		if !pool.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("TLS CA %q contains no PEM certificate", o.CACert)
		}
		conf.RootCAs = pool
	}

	if (o.ClientCert == "") != (o.ClientKey == "") {
		return nil, fmt.Errorf("TLS client certificate and key must be given together")
	}
	if o.ClientCert != "" {
		cert, err := tls.LoadX509KeyPair(o.ClientCert, o.ClientKey)
		if err != nil {
			return nil, fmt.Errorf("load TLS client certificate %q with key %q: %w", o.ClientCert, o.ClientKey, err)
		}
		conf.Certificates = []tls.Certificate{cert}
	}

	return credentials.NewTLS(conf), nil
}

// TLS environment variables, mirroring the flags on sds.
const (
	envTLS           = "SDS_TLS"
	envTLSCACert     = "SDS_TLS_CA"
	envTLSClientCert = "SDS_TLS_CERT"
	envTLSClientKey  = "SDS_TLS_KEY"
	envTLSServerName = "SDS_TLS_SERVER_NAME"
	envTLSInsecure   = "SDS_TLS_INSECURE"
)

// ResolveTLS fills in whatever the caller did not pass explicitly from the
// environment, the same flag-beats-env precedence ResolveToken uses. It lets a
// TLS cluster be driven from a shell profile or a systemd unit without every
// command repeating four flags.
func ResolveTLS(explicit TLSOptions) TLSOptions {
	o := explicit
	if o.CACert == "" {
		o.CACert = os.Getenv(envTLSCACert)
	}
	if o.ClientCert == "" {
		o.ClientCert = os.Getenv(envTLSClientCert)
	}
	if o.ClientKey == "" {
		o.ClientKey = os.Getenv(envTLSClientKey)
	}
	if o.ServerName == "" {
		o.ServerName = os.Getenv(envTLSServerName)
	}
	if !o.Enabled {
		o.Enabled = envBool(envTLS)
	}
	if !o.Insecure {
		o.Insecure = envBool(envTLSInsecure)
	}
	return o
}

// envBool treats an unparsable value as unset rather than as true: SDS_TLS=no
// must not enable TLS just because the string is non-empty.
func envBool(name string) bool {
	v, err := strconv.ParseBool(os.Getenv(name))
	return err == nil && v
}
