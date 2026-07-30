package wanproxy

import (
	"strings"
	"testing"
)

// Pinning the egress source is how an operator puts WAN replication on a chosen
// uplink (a leased line rather than the management link) and gives the DR
// firewall a predictable source IP to allow.
func TestDialerConfigPinsEgressWhenRequested(t *testing.T) {
	spec := sampleSpec()
	spec.PrimaryEgressAddr = "203.0.113.10"

	cfg := RenderDialerConfig(spec)

	// Port 0 is essential: the kernel picks an ephemeral port. A fixed port would
	// make a reconnect fail with EADDRINUSE while the old socket is in TIME_WAIT.
	want := `bind_addr = "203.0.113.10:0"`
	if !strings.Contains(cfg, want) {
		t.Fatalf("dialer config should contain %s:\n%s", want, cfg)
	}
}

// Only the dialer dials out; the acceptor binds wan_listen, so an egress there
// would be meaningless (and, if honoured, wrong).
func TestAcceptorConfigNeverPinsEgress(t *testing.T) {
	spec := sampleSpec()
	spec.PrimaryEgressAddr = "203.0.113.10"

	if cfg := RenderAcceptorConfig(spec); strings.Contains(cfg, "bind_addr") {
		t.Fatalf("acceptor config must not set bind_addr:\n%s", cfg)
	}
}

// Unset (and whitespace-only) must emit nothing, so an existing deployment's
// rendered config is byte-identical to before this option existed.
func TestNoEgressEmitsNoBindAddr(t *testing.T) {
	for name, addr := range map[string]string{"empty": "", "whitespace": "   "} {
		t.Run(name, func(t *testing.T) {
			spec := sampleSpec()
			spec.PrimaryEgressAddr = addr
			if cfg := RenderDialerConfig(spec); strings.Contains(cfg, "bind_addr") {
				t.Errorf("no egress requested, but bind_addr was emitted:\n%s", cfg)
			}
		})
	}
}
