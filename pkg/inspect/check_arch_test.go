package inspect

import (
	"testing"
)

// A controller binary built for another architecture installs fine and never
// starts; the hash comparison cannot see it, since it compares within one
// architecture.
func TestBinaryArchMismatchFails(t *testing.T) {
	in := &Input{Probes: map[string]*NodeProbe{
		"a": {Arch: "x86_64", CtlMachine: "3e00", CtlBin: "/opt/haify/bin/haify-controller"},
		"b": {Arch: "aarch64", CtlMachine: "3e00", CtlBin: "/opt/haify/bin/haify-controller"},
		"c": {Arch: "aarch64", CtlMachine: "b700"},
		"d": {Arch: "x86_64"},
	}}
	got := binaryArchChecks(in)
	if len(got) != 1 || got[0].Subject != "b" || got[0].Status != StatusFail {
		t.Fatalf("want one failure for b, got %+v", got)
	}
	p, err := ParseProbe("probe=1\narch=aarch64\nctl_machine=b700\nend=1\n")
	if err != nil || p.CtlMachine != "b700" {
		t.Fatalf("ctl_machine not parsed: %+v %v", p, err)
	}
}
