package inspect

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"
)

// Certificate expiry thresholds.
const (
	certWarn = 30 * 24 * time.Hour
	certFail = 7 * 24 * time.Hour
)

// checkTLS judges certificate expiry: the API server certificate, the
// replication CA, and every node's replication certificate.
func checkTLS(in *Input) []Check {
	t := in.TLS
	var out []Check
	seen := 0
	if t.APICertErr != "" {
		out = append(out, Check{ID: "tls.api_cert", Area: AreaTLS, Subject: t.APICertPath, Status: StatusError,
			Message: "the API server certificate could not be read: " + t.APICertErr})
	}
	if t.APICert != nil {
		seen++
		if c, ok := expiry(in, "tls.api_cert", t.APICertPath, "API server certificate", t.APICert,
			"replace "+t.APICertPath+" and restart sds-controller"); ok {
			out = append(out, c)
		}
	}
	if t.ReplicationCA != nil {
		seen++
		if c, ok := expiry(in, "tls.replication_ca", t.ReplicationCAPath, "replication CA", t.ReplicationCA,
			"sds replication-tls setup"); ok {
			out = append(out, c)
		}
	}
	for _, node := range sortedKeys(in.Probes) {
		raw := in.Probes[node].TLSCert
		if len(raw) == 0 {
			continue
		}
		seen++
		cert, err := parsePEMCert(raw)
		if err != nil {
			out = append(out, Check{ID: "tls.replication_cert", Area: AreaTLS, Subject: node, Status: StatusFail,
				Message: "/etc/sds/drbd-tls/node.crt is unreadable: " + err.Error(), Fix: "sds replication-tls setup"})
			continue
		}
		if c, ok := expiry(in, "tls.replication_cert", node, "replication certificate", cert, "sds replication-tls setup"); ok {
			out = append(out, c)
		}
	}
	if len(out) == 0 && seen == 0 {
		out = append(out, pass("tls.expiry", AreaTLS, "no TLS certificates in use"))
	}
	if len(out) == 0 {
		out = append(out, pass("tls.expiry", AreaTLS, "%s valid for more than %d days",
			plural(seen, "certificate", "certificates"), int(certWarn.Hours()/24)))
	}
	return out
}

func expiry(in *Input, id, subject, what string, cert *x509.Certificate, fix string) (Check, bool) {
	left := cert.NotAfter.Sub(in.Now)
	if left >= certWarn {
		return Check{}, false
	}
	c := Check{ID: id, Area: AreaTLS, Subject: subject, Status: StatusWarn, Fix: fix,
		Evidence: []string{"not after " + cert.NotAfter.UTC().Format(time.RFC3339), "subject " + cert.Subject.String()}}
	switch {
	case left <= 0:
		c.Status = StatusFail
		c.Message = fmt.Sprintf("%s expired %s ago", what, (-left).Round(time.Hour))
	case left < certFail:
		c.Status = StatusFail
		c.Message = fmt.Sprintf("%s expires in %s", what, left.Round(time.Hour))
	default:
		c.Message = fmt.Sprintf("%s expires in %d days", what, int(left.Hours()/24))
	}
	return c, true
}

// parsePEMCert reads the first certificate of a PEM file.
func parsePEMCert(raw []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("no PEM block")
	}
	return x509.ParseCertificate(block.Bytes)
}

// ParseCertFile reads the first certificate of PEM data; exported for the
// controller, which reads the API and CA certificates locally.
func ParseCertFile(raw []byte) (*x509.Certificate, error) { return parsePEMCert(raw) }
