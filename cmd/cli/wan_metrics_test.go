package main

import (
	"io"
	"os"
	"strings"
	"testing"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
)

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		in   uint64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1048576, "1.0 MiB"},
		{4294967296, "4.0 GiB"},
	}
	for _, tt := range tests {
		if got := humanBytes(tt.in); got != tt.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// The distinction that matters: a missing snapshot must read as UNKNOWN. Printing
// a zero backlog would tell an operator a DR failover loses nothing when we
// simply have no idea — the most consequential way to be wrong here.
func TestPrintWANMetricsSaysUnknownRatherThanZero(t *testing.T) {
	out := captureStdout(t, func() { printWANMetrics(nil) })

	if !strings.Contains(out, "unknown") {
		t.Errorf("absent metrics must be reported as unknown, got: %q", out)
	}
	if strings.Contains(out, "0 B") {
		t.Errorf("must not imply a zero backlog when unknown, got: %q", out)
	}
}

func TestPrintWANMetricsWarnsAboutTheLossWindow(t *testing.T) {
	out := captureStdout(t, func() {
		printWANMetrics(&sdspb.WANMetrics{
			BufferUsedBytes:   1048576,
			BufferCapBytes:    4294967296,
			BufferFillPercent: 0.024,
			DrbdToWanBytes:    900,
			WanWireBytes:      300,
			CompressionRatio:  3.0,
			Reconnects:        2,
			RingFullEvents:    1,
		})
	})

	for _, want := range []string{
		"1.0 MiB",     // the backlog, human-readable
		"4.0 GiB",     // buffer capacity
		"would lose",  // the consequence spelled out
		"3.00x",       // compression actually achieved
		"reconnects",  // flapping-link hint
		"Buffer full", // WAN could not keep up
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// A healthy link should stay quiet: no loss warning, and no counters that are
// only meaningful when non-zero.
func TestPrintWANMetricsQuietWhenHealthy(t *testing.T) {
	out := captureStdout(t, func() {
		printWANMetrics(&sdspb.WANMetrics{
			BufferUsedBytes: 0,
			BufferCapBytes:  4294967296,
			DrbdToWanBytes:  4096,
			WanWireBytes:    2048,
			WanConnected:    true,
		})
	})

	for _, unwanted := range []string{"would lose", "reconnects", "Buffer full"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("healthy link should not mention %q:\n%s", unwanted, out)
		}
	}
	if !strings.Contains(out, "Un-replicated") {
		t.Errorf("the backlog line should still be shown:\n%s", out)
	}
}

// captureStdout runs f with os.Stdout redirected and returns what it printed.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()

	f()
	_ = w.Close()
	return <-done
}
