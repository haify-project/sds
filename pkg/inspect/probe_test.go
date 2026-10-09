package inspect

import (
	"encoding/base64"
	"os/exec"
	"strings"
	"testing"
)

const drbdJSON = `[{"name":"vm","node-id":0,"role":"Primary","suspended":false,
 "devices":[{"volume":0,"disk-state":"UpToDate","quorum":true}],
 "connections":[{"peer-node-id":1,"name":"host-n2","connection-state":"Connected","peer-role":"Secondary",
   "peer_devices":[{"volume":0,"replication-state":"WFBitMapS","peer-disk-state":"Outdated","out-of-sync":4096}]}]}]`

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestParseProbe(t *testing.T) {
	out := strings.Join([]string{
		"probe=1",
		"hostname=host-n1",
		"now=1790917200.250000000",
		"ntp=yes",
		"rootfs=91",
		"addr=127.0.0.1", "addr=10.0.0.1", "addr=fe80::1",
		"drbd_kmod=9.2.12",
		"drbd_utils=9.29.0",
		"reactor=1.5.0",
		"reactor_active=active",
		"ctl_active=active",
		"ctl_bin=/opt/haify/bin/haify-controller 0123abcd",
		"reactor_conf=haify-nfs-share.toml", "reactor_conf=haify-ha-haify-meta.toml.disabled",
		"res_file=vm", "res_file=share",
		"hosts=10.0.0.2 host-n2 n2",
		"tls_cert=" + b64("-----BEGIN CERTIFICATE-----"),
		"lvs=" + b64("  haify_pool0|thinpool|thin-pool|107374182400|61.25|8.50\n  haify_pool0|vm_data|thin|10737418240||\n"),
		"drbd=" + b64(drbdJSON),
		"end=1",
	}, "\n")
	p, err := ParseProbe("Warning: something on stderr\n" + out)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Complete || p.Hostname != "host-n1" || p.RootUse != 91 || p.Now != 1790917200.25 || p.NTPSynced != "yes" {
		t.Errorf("scalars: %+v", p)
	}
	if !p.HasAddr("10.0.0.1") || p.HasAddr("10.0.0.2") {
		t.Errorf("addrs: %v", p.Addrs)
	}
	if p.CtlBin != "/opt/haify/bin/haify-controller" || p.CtlSHA != "0123abcd" {
		t.Errorf("controller binary: %q %q", p.CtlBin, p.CtlSHA)
	}
	if !p.HasReactorConf("haify-nfs-share.toml") || p.HasReactorConf("haify-ha-haify-meta.toml") {
		t.Errorf("reactor confs: %v", p.ReactorConf)
	}
	if len(p.Hosts) != 1 || p.Hosts[0].IP != "10.0.0.2" || len(p.Hosts[0].Names) != 2 {
		t.Errorf("hosts: %+v", p.Hosts)
	}
	if !p.LVsOK || len(p.LVs) != 2 || !p.LVs[0].HasUsage || p.LVs[0].DataPercent != 61.25 || p.LVs[1].HasUsage {
		t.Errorf("lvs: %+v", p.LVs)
	}
	v := p.Resource("vm")
	if !p.DRBDOK || v == nil || v.Connections[0].PeerDevices[0].ReplicationState != "WFBitMapS" {
		t.Errorf("drbd: %+v", p.DRBD)
	}
}

// A damaged section is noted and the rest kept; a cut-off probe still yields
// what arrived.
func TestParseProbeTolerance(t *testing.T) {
	p, err := ParseProbe("probe=1\nrootfs=12\ndrbd=" + b64("[{not json") + "\nlvs=%%%")
	if err != nil {
		t.Fatal(err)
	}
	if p.Complete || p.RootUse != 12 || p.DRBDOK || p.LVsOK || len(p.Problems) != 2 {
		t.Errorf("got %+v", p)
	}
	if _, err := ParseProbe("sudo: a password is required"); err == nil {
		t.Error("output that is not the probe's must be rejected")
	}
	p, err = ParseProbe("probe=1\ndrbd=\nend=1")
	if err != nil || !p.DRBDOK || len(p.DRBD) != 0 {
		t.Errorf("no resources up is an empty, readable view: %+v %v", p, err)
	}
}

// The script is shipped base64-wrapped to bash; a syntax error would only
// show on a live node.
func TestProbeScriptIsValidBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	cmd := exec.Command(bash, "-n")
	cmd.Stdin = strings.NewReader(ProbeScript)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}
