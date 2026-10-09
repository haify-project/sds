package inspect

import (
	"strings"
	"testing"
)

// Findings from the first runs on real clusters that were noise or wrong.

func TestControllerBinariesComparedPerArchitecture(t *testing.T) {
	in := cluster()
	in.SelfHA = &SelfHAInput{Resource: "haify-meta"}
	in.Probes["n1"].Arch, in.Probes["n1"].CtlSHA = "aarch64", "arm"
	in.Probes["n2"].Arch, in.Probes["n2"].CtlSHA = "x86_64", "amd"
	in.Probes["n3"].Arch, in.Probes["n3"].CtlSHA = "x86_64", "amd"
	if got := find(checkNodes(in), "nodes.controller_binary"); len(got) != 0 {
		t.Fatalf("different architectures never hash alike:\n%s", dump(got))
	}
	in.Probes["n3"].CtlSHA = "amd-old"
	c := only(t, checkNodes(in), "nodes.controller_binary")
	if c.Subject != "x86_64" || c.Fix != "./scripts/deploy-all.sh n2,n3" {
		t.Errorf("got %+v", c)
	}
}

// A cloud VM registered at its public address, reached through NAT.
func TestPublicAddressBehindNATIsNotRenumbered(t *testing.T) {
	in := cluster()
	in.Nodes[2].Address = "47.109.108.170"
	in.Probes["n3"].Addrs = []string{"127.0.0.1", "172.16.176.149"}
	checks := checkNodes(in)
	c := only(t, checks, "nodes.address")
	if c.Status != StatusPass || c.Fix != "" || !strings.Contains(c.Message, "NAT") {
		t.Errorf("got %+v", c)
	}
	only(t, checks, "nodes.health")

	// The same public address answering as another host is not NAT.
	in.Probes["n3"].Hostname = "stranger"
	if c := only(t, checkNodes(in), "nodes.address"); c.Status != StatusWarn {
		t.Errorf("got %+v", c)
	}
	// A private address missing from the interfaces is DHCP drift: keep the fix.
	in = cluster()
	in.Nodes[2].Address = "100.64.0.9"
	in.Probes["n3"].Addrs = []string{"100.64.0.12"}
	if c := only(t, checkNodes(in), "nodes.address"); c.Fix != "haify node set-address n3 100.64.0.12" {
		t.Errorf("got %+v", c)
	}
}

func TestDuplicateHostsLinesReportedOnce(t *testing.T) {
	in := cluster()
	e := HostsEntry{IP: "10.0.0.99", Names: []string{"host-n1"}}
	in.Probes["n3"].Hosts = []HostsEntry{e, e}
	if got := find(checkNodes(in), "nodes.hosts_entry"); len(got) != 1 {
		t.Errorf("want one item:\n%s", dump(got))
	}
}

// node-a carries the haify-meta promoter while diskless, on purpose.
func TestSelfHAPromoterOnDisklessNodeWarns(t *testing.T) {
	in := cluster()
	selfHA(in)
	in.Resources[0].Diskful = []string{"n1", "n2"}
	in.Resources[0].Clients = []string{"n3"}
	c := only(t, checkSelfHA(in), "selfha.promoter_on_diskless")
	if c.Status != StatusWarn || !strings.Contains(c.Message, "diskless Primary") {
		t.Errorf("got %+v", c)
	}
}

// Thick haify_vg0 with 250 extents (1000 MiB) free cannot reserve the 2 GiB a
// snapshot of a 10 GiB volume needs: the failed scheduled backups.
func TestThickPoolSnapshotRoom(t *testing.T) {
	in := cluster()
	in.Probes["n1"].VGFree = map[string]uint64{"haify_vg0": 250 * 4 << 20}
	in.Probes["n1"].LVs = []LV{
		{VG: "haify_vg0", Name: "pve-9001-0_data", Segtype: "linear", SizeBytes: 10 << 30},
		{VG: "haify_vg0", Name: "small_data", Segtype: "linear", SizeBytes: 1 << 30},
		{VG: "haify_vg0", Name: "pve-9001-0_data_sched_20261001T020000Z", Segtype: "snapshot", SizeBytes: 2 << 30},
	}
	checks := checkPools(in)
	c := only(t, checks, "pool.snapshot_room")
	if c.Subject != "n1:haify_vg0" || len(c.Evidence) != 2 || !strings.Contains(c.Evidence[1], "pve-9001-0_data") {
		t.Errorf("only the 10 GiB volume is short (1 GiB needs 256 MiB): %+v", c)
	}
	if c.Fix != "haify pool add --pool vg0 --nodes n1 --devices <new-device>" {
		t.Errorf("fix = %q", c.Fix)
	}
	in.Probes["n1"].VGFree["haify_vg0"] = 4 << 30
	if c := only(t, checkPools(in), "pool.usage"); !strings.Contains(c.Message, "1 thick pool has room") {
		t.Errorf("got %+v", c)
	}
	if cowBytes(10<<30) != 2048<<20 || cowBytes(512<<20) != 256<<20 {
		t.Error("cowBytes must match the controller's reservation")
	}
}

func TestNothingToCountSaysSo(t *testing.T) {
	in := cluster()
	for want, c := range map[string]Check{
		"no pools":                        only(t, checkPools(in), "pool.usage"),
		"no TLS certificates in use":      only(t, checkTLS(in), "tls.expiry"),
		"no gateways":                     only(t, checkGateways(in), "gateway.serving"),
		"no backup or snapshot schedules": only(t, checkBackups(in), "backups.schedules"),
		"no resources":                    only(t, checkResources(in), "resource.replicas"),
	} {
		if c.Message != want {
			t.Errorf("%s: %q", c.ID, c.Message)
		}
	}
}

func TestProbeCarriesArchAndVGFree(t *testing.T) {
	p, err := ParseProbe("probe=1\narch=aarch64\nvg_free=haify_vg0 1048576000\nvg_free=bad\nend=1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Arch != "aarch64" || p.VGFree["haify_vg0"] != 1048576000 || len(p.VGFree) != 1 {
		t.Errorf("got %+v", p)
	}
	if !strings.Contains(ProbeScript, "uname -m") || !strings.Contains(ProbeScript, "vg_free") {
		t.Error("the probe must gather the architecture and VG free space")
	}
}
