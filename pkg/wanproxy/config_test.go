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
metrics_path = "/run/sds-proxy/data.json"
metrics_interval_secs = 5

[tls]
ca = "/etc/sds-proxy/ca.pem"
cert = "/etc/sds-proxy/cert.pem"
key = "/etc/sds-proxy/key.pem"
peer_name = "sds-proxy"
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
metrics_path = "/run/sds-proxy/data.json"
metrics_interval_secs = 5
acceptor_park_secs = 30
synthesize_ping_acks = true

[tls]
ca = "/etc/sds-proxy/ca.pem"
cert = "/etc/sds-proxy/cert.pem"
key = "/etc/sds-proxy/key.pem"
peer_name = "sds-proxy"
`
	if got != want {
		t.Fatalf("acceptor config mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestConfigDeterministic guards the table-test contract: rendering the same
// spec twice yields identical bytes.
func TestConfigDeterministic(t *testing.T) {
	spec := sampleSpec()
	if RenderDialerConfig(spec) != RenderDialerConfig(spec) {
		t.Fatal("dialer config not deterministic")
	}
	if RenderAcceptorConfig(spec) != RenderAcceptorConfig(spec) {
		t.Fatal("acceptor config not deterministic")
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
