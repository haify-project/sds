package wanproxy

import "testing"

// sampleSpec is the canonical spec the config tests render against. P (DRBDPort)
// is 7900, WAN port 37901, DR endpoint mirrors the aliyun<->orange topology.
func sampleSpec() ProxySpec {
	return ProxySpec{
		Resource:         "data",
		PrimaryNodeAddr:  "10.0.0.1",
		DRNodeAddr:       "10.0.0.2",
		DRPublicEndpoint: "47.109.108.170",
		WANPort:          37901,
		DRBDPort:         7900,
	}
}

func TestRenderDialerConfig(t *testing.T) {
	got := RenderDialerConfig(sampleSpec())
	want := `role = "dialer"
drbd_listen = "127.0.0.1:7900"
peer = "47.109.108.170:37901"
wan_listen = "0.0.0.0:37901"
bulk_cap_bytes = 4294967296
zstd_level = 3
zstd_min_size = 64
on_congestion = "pull-ahead"
overflow_grace_secs = 30
metrics_path = "/run/haify-proxy/data.json"
metrics_interval_secs = 5

[tls]
ca = "/etc/haify-proxy/ca.pem"
cert = "/etc/haify-proxy/cert.pem"
key = "/etc/haify-proxy/key.pem"
peer_name = "haify-proxy"
`
	if got != want {
		t.Fatalf("dialer config mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderAcceptorConfig(t *testing.T) {
	got := RenderAcceptorConfig(sampleSpec())
	want := `role = "acceptor"
drbd_listen = "127.0.0.1:7900"
peer = "0.0.0.0:37901"
wan_listen = "0.0.0.0:37901"
bulk_cap_bytes = 4294967296
zstd_level = 3
zstd_min_size = 64
on_congestion = "pull-ahead"
overflow_grace_secs = 30
metrics_path = "/run/haify-proxy/data.json"
metrics_interval_secs = 5
acceptor_park_secs = 30
synthesize_ping_acks = true

[tls]
ca = "/etc/haify-proxy/ca.pem"
cert = "/etc/haify-proxy/cert.pem"
key = "/etc/haify-proxy/key.pem"
peer_name = "haify-proxy"
`
	if got != want {
		t.Fatalf("acceptor config mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestConfigDeterministic guards the contract the golden tests above depend on:
// rendering the same spec repeatedly yields identical bytes.
//
// Both renders are bound to variables rather than compared inline. Comparing
// two calls in one expression reads as a tautology — to a reviewer and to
// staticcheck (SA4000) alike — which buries what is actually being asserted.
//
// The comparison is also repeated, because the realistic way these renderers
// lose determinism is a map range creeping into the option emission. Go
// randomizes map iteration order per range, so one extra render agrees with the
// first by chance often enough to let such a change through; a run of them does
// not. The renderers are pure string building, so the loop costs microseconds.
//
// Determinism matters beyond tidiness: the provisioner writes these files to
// both nodes and reloads haify-proxy when the content changes. A renderer that
// reorders its own keys would make every reconciliation look like a config
// change and bounce the WAN legs on a loop.
func TestConfigDeterministic(t *testing.T) {
	spec := sampleSpec()
	dialer := RenderDialerConfig(spec)
	acceptor := RenderAcceptorConfig(spec)

	const renders = 100
	for i := 2; i <= renders; i++ {
		if got := RenderDialerConfig(spec); got != dialer {
			t.Fatalf("dialer config not deterministic; render %d differs\n--- first ---\n%s\n--- render %d ---\n%s", i, dialer, i, got)
		}
		if got := RenderAcceptorConfig(spec); got != acceptor {
			t.Fatalf("acceptor config not deterministic; render %d differs\n--- first ---\n%s\n--- render %d ---\n%s", i, acceptor, i, got)
		}
	}
}

func TestProxySpecValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ProxySpec)
		wantErr bool
	}{
		{"valid", func(*ProxySpec) {}, false},
		{"no resource", func(s *ProxySpec) { s.Resource = "" }, true},
		{"no primary", func(s *ProxySpec) { s.PrimaryNodeAddr = "" }, true},
		{"no dr node", func(s *ProxySpec) { s.DRNodeAddr = "" }, true},
		{"no endpoint", func(s *ProxySpec) { s.DRPublicEndpoint = "" }, true},
		{"bad wan port", func(s *ProxySpec) { s.WANPort = 0 }, true},
		{"bad drbd port", func(s *ProxySpec) { s.DRBDPort = 70000 }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := sampleSpec()
			tt.mutate(&spec)
			err := spec.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
