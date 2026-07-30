package wanproxy

import (
	"context"
	"strings"
	"testing"
)

// The controller reads sds-proxy's published snapshot to answer the one question
// protocol A makes urgent: how much data would a DR failover lose? A snapshot it
// cannot read must therefore come back as UNKNOWN (nil), never as a zeroed
// struct — reporting a zero backlog when the truth is unknown would tell an
// operator the failover is free when it may not be.
func TestParseMetricsRejectsUnusableInput(t *testing.T) {
	tests := []struct {
		name, in string
	}{
		{"empty", ""},
		{"whitespace only", "  \n "},
		{"shell error instead of json", "cat: /run/sds-proxy/r.json: No such file or directory"},
		{"truncated document", `{"buffer_used_bytes": 12`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if m := ParseMetrics(tt.in); m != nil {
				t.Fatalf("want nil (unknown), got %+v", m)
			}
		})
	}
}

// Field-for-field against the document sds_proxy::metrics::Snapshot serializes.
// If either side renames a field this test is what catches it.
func TestParseMetricsReadsPublishedSnapshot(t *testing.T) {
	const body = `{
  "buffer_used_bytes": 1048576,
  "buffer_cap_bytes": 4294967296,
  "buffer_fill_percent": 0.0244140625,
  "drbd_to_wan_bytes": 900,
  "wan_wire_bytes": 300,
  "wan_to_drbd_bytes": 120,
  "frames_sent": 7,
  "compression_ratio": 3.0,
  "wan_connected": true,
  "reconnects": 2,
  "ring_full_events": 1
}`
	m := ParseMetrics(body)
	if m == nil {
		t.Fatal("ParseMetrics returned nil for a valid snapshot")
	}
	checks := []struct {
		field string
		got   uint64
		want  uint64
	}{
		{"buffer_used_bytes", m.BufferUsedBytes, 1048576},
		{"buffer_cap_bytes", m.BufferCapBytes, 4294967296},
		{"drbd_to_wan_bytes", m.DRBDToWANBytes, 900},
		{"wan_wire_bytes", m.WANWireBytes, 300},
		{"wan_to_drbd_bytes", m.WANToDRBDBytes, 120},
		{"frames_sent", m.FramesSent, 7},
		{"reconnects", m.Reconnects, 2},
		{"ring_full_events", m.RingFullEvents, 1},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.field, c.got, c.want)
		}
	}
	if m.CompressionRat != 3.0 {
		t.Errorf("compression_ratio = %v, want 3.0", m.CompressionRat)
	}
	if !m.WANConnected {
		t.Error("wan_connected should be true")
	}
}

func TestStatusIncludesPrimaryMetrics(t *testing.T) {
	spec := sampleSpec()
	f := &fakeDeploy{
		execOutput: map[string]string{
			"cat " + NodeMetricsPath(spec.Resource): `{"buffer_used_bytes":2048,"buffer_cap_bytes":4096,"wan_connected":true}`,
		},
	}

	st, err := Status(context.Background(), f, spec)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.PrimaryMetrics == nil {
		t.Fatal("the primary's published snapshot should be surfaced")
	}
	if st.PrimaryMetrics.BufferUsedBytes != 2048 {
		t.Errorf("BufferUsedBytes = %d, want 2048", st.PrimaryMetrics.BufferUsedBytes)
	}
	if st.PrimaryMetrics.BufferCapBytes != 4096 {
		t.Errorf("BufferCapBytes = %d, want 4096", st.PrimaryMetrics.BufferCapBytes)
	}

	// The snapshot must be read from the PRIMARY: that is the side holding the
	// backlog, so reading the DR's copy would report a permanently empty queue.
	var read *event
	for i := range f.events {
		if strings.Contains(f.events[i].cmd, "cat "+NodeMetricsDir) {
			read = &f.events[i]
			break
		}
	}
	if read == nil {
		t.Fatal("no metrics read was issued")
	}
	if !sameHosts(read.hosts, []string{spec.PrimaryNodeAddr}) {
		t.Errorf("metrics read ran on %v, want just the primary %q", read.hosts, spec.PrimaryNodeAddr)
	}
}

// An older proxy build publishes nothing at all. Status must still succeed: the
// resource is healthy, we simply cannot report its backlog. Failing here would
// make an observability gap look like a broken resource.
func TestStatusToleratesMissingMetricsFile(t *testing.T) {
	spec := sampleSpec()
	f := &fakeDeploy{
		execFailSubstrOutput: map[string]string{
			"cat " + NodeMetricsDir: "cat: no such file or directory",
		},
	}

	st, err := Status(context.Background(), f, spec)
	if err != nil {
		t.Fatalf("a missing snapshot must not fail Status: %v", err)
	}
	if st.PrimaryMetrics != nil {
		t.Errorf("want nil metrics, got %+v", st.PrimaryMetrics)
	}
	if !st.Primary.Active || !st.DR.Active {
		t.Error("the rest of the status should still be reported")
	}
}

// The generated proxy config must point the proxy at the path Status reads back;
// a mismatch would silently mean permanently unknown metrics.
func TestRenderedConfigsPublishToThePathStatusReads(t *testing.T) {
	spec := sampleSpec()
	want := "metrics_path = \"" + NodeMetricsPath(spec.Resource) + "\""

	for name, cfg := range map[string]string{
		"dialer":   RenderDialerConfig(spec),
		"acceptor": RenderAcceptorConfig(spec),
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("%s config does not publish to the path Status reads (%s):\n%s", name, want, cfg)
		}
		if !strings.Contains(cfg, "metrics_interval_secs") {
			t.Errorf("%s config is missing metrics_interval_secs:\n%s", name, cfg)
		}
	}
}
