package controller

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/haify/pkg/drbdtls"
)

func nodeCertFor(t *testing.T, ca *drbdtls.CA, node string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: node}}, key)
	require.NoError(t, err)
	cert, err := ca.Sign(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), node, nil)
	require.NoError(t, err)
	return cert
}

func statusOutput(tlshd string, conf, module bool, trust, cert []byte) string {
	var b strings.Builder
	fmt.Fprintf(&b, "HAIFY_TLSHD=%s\n", tlshd)
	if conf {
		b.WriteString("HAIFY_CONF=yes\n")
	}
	if module {
		b.WriteString("HAIFY_MODULE=yes\n")
	}
	if trust != nil {
		fmt.Fprintf(&b, "HAIFY_TRUST=%s\n", base64.StdEncoding.EncodeToString(trust))
	}
	if cert != nil {
		fmt.Fprintf(&b, "HAIFY_CERT=%s\n", base64.StdEncoding.EncodeToString(cert))
	}
	return b.String()
}

func TestJudgeNodeTLS(t *testing.T) {
	ca, err := drbdtls.Ensure(t.TempDir())
	require.NoError(t, err)
	other, err := drbdtls.Ensure(t.TempDir())
	require.NoError(t, err)
	good := nodeCertFor(t, ca, "n1")
	now := time.Now()

	st := judgeNodeTLS(ca, "n1", statusOutput("active", true, true, ca.CertPEM, good), now)
	assert.True(t, st.Ready, st.Problem)

	cases := map[string]struct {
		out  string
		want string
	}{
		"never set up":         {statusOutput("inactive", false, false, nil, nil), "no replication certificate"},
		"tlshd stopped":        {statusOutput("inactive", true, true, ca.CertPEM, good), "tlshd is inactive"},
		"no module":            {statusOutput("active", true, false, ca.CertPEM, good), "tls module"},
		"another CA trusted":   {statusOutput("active", true, true, other.CertPEM, good), "trust store"},
		"another CA's cert":    {statusOutput("active", true, true, ca.CertPEM, nodeCertFor(t, other, "n1")), "not accepted"},
		"another node's cert":  {statusOutput("active", true, true, ca.CertPEM, nodeCertFor(t, ca, "n2")), "not accepted"},
		"tlshd conf elsewhere": {statusOutput("active", false, true, ca.CertPEM, good), "tlshd.conf"},
	}
	for name, c := range cases {
		st := judgeNodeTLS(ca, "n1", c.out, now)
		assert.False(t, st.Ready, name)
		assert.Contains(t, st.Problem, c.want, name)
	}

	// A certificate about to lapse is flagged before connections start failing.
	st = judgeNodeTLS(ca, "n1", statusOutput("active", true, true, ca.CertPEM, good), now.Add(5*365*24*time.Hour-10*24*time.Hour))
	assert.False(t, st.Ready)
	assert.Contains(t, st.Problem, "expires")
}

func TestTLSMembersIsTheWholeMesh(t *testing.T) {
	info := &ResourceInfo{Nodes: []string{"a", "b"}, DisklessNodes: []string{"t"}, DisklessClients: []string{"c", "a"}}
	assert.Equal(t, []string{"a", "b", "t", "c"}, tlsMembers(info))
}
