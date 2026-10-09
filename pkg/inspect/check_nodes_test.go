package inspect

import (
	"strings"
	"testing"
)

func TestNodesHealthyIsOnePassLine(t *testing.T) {
	checks := checkNodes(cluster())
	allPass(t, checks)
}

// A node that does not answer is one failed check plus unknowns, never a
// failed run.
func TestUnreachableNodeIsAFailNotACrash(t *testing.T) {
	in := cluster()
	delete(in.Probes, "n2")
	in.ProbeErrors["n2"] = "dial tcp 10.0.0.2:22: i/o timeout"
	checks := Run(in, nil)
	c := only(t, checks, "nodes.ssh")
	if c.Subject != "n2" || c.Status != StatusFail || c.Runbook != "renumber-nodes" {
		t.Errorf("got %+v", c)
	}
}

func TestClockSkewIsMeasuredAgainstTheWholeWindow(t *testing.T) {
	in := cluster()
	start := float64(in.ProbeStart.Unix())
	in.Probes["n1"].Now = start + 1.9   // inside the window: latency, not skew
	in.Probes["n2"].Now = start + 2 + 5 // 5s past the window's end
	in.Probes["n3"].Now = start - 45    // 45s before the window started
	in.Probes["n3"].NTPSynced = "no"
	checks := checkNodes(in)
	clocks := find(checks, "nodes.clock")
	if len(clocks) != 2 {
		t.Fatalf("want n2 and n3 flagged:\n%s", dump(checks))
	}
	for _, c := range clocks {
		switch c.Subject {
		case "n2":
			if c.Status != StatusWarn || !strings.Contains(c.Message, "ahead") {
				t.Errorf("n2: %+v", c)
			}
		case "n3":
			if c.Status != StatusFail || !strings.Contains(c.Message, "behind") {
				t.Errorf("n3: %+v", c)
			}
		}
	}
	only(t, checks, "nodes.ntp")
}

func TestRootFilesystemThresholds(t *testing.T) {
	in := cluster()
	in.Probes["n1"].RootUse = 85
	in.Probes["n2"].RootUse = 97
	checks := find(checkNodes(in), "nodes.root_fs")
	if len(checks) != 2 {
		t.Fatalf("got:\n%s", dump(checks))
	}
	for _, c := range checks {
		want := map[string]Status{"n1": StatusWarn, "n2": StatusFail}[c.Subject]
		if c.Status != want {
			t.Errorf("%s: %s, want %s", c.Subject, c.Status, want)
		}
	}
}

// After DHCP the registered address is gone from the node, and another
// machine may answer on it.
func TestAddressDriftAndIdentity(t *testing.T) {
	in := cluster()
	in.Probes["n1"].Addrs = []string{"127.0.0.1", "fe80::1", "10.0.0.41"}
	in.Probes["n2"].Hostname = "someone-else"
	checks := checkNodes(in)
	c := only(t, checks, "nodes.address")
	if c.Subject != "n1" || c.Fix != "haify node set-address n1 10.0.0.41" {
		t.Errorf("got %+v", c)
	}
	if c := only(t, checks, "nodes.identity"); c.Subject != "n2" || c.Status != StatusFail {
		t.Errorf("got %+v", c)
	}
}

func TestDRBDStackAndVersions(t *testing.T) {
	in := cluster()
	in.Probes["n1"].DRBDKmod = ""
	in.Probes["n2"].DRBDUtils = "9.27.0"
	in.Probes["n3"].ReactorUp = "failed"
	checks := checkNodes(in)
	if c := only(t, checks, "nodes.drbd_module"); c.Fix != "ssh 10.0.0.1 sudo modprobe drbd" {
		t.Errorf("got %+v", c)
	}
	if c := only(t, checks, "nodes.drbd_utils_version"); len(c.Evidence) != 2 {
		t.Errorf("evidence should group nodes by version: %v", c.Evidence)
	}
	only(t, checks, "nodes.reactor")
}

func TestControllerBinariesDiffer(t *testing.T) {
	in := cluster()
	in.Probes["n1"].CtlSHA, in.Probes["n1"].CtlBin = "aaa", "/opt/haify/bin/haify-controller"
	in.Probes["n2"].CtlSHA, in.Probes["n2"].CtlBin = "bbb", "/opt/haify/bin/haify-controller"
	c := only(t, checkNodes(in), "nodes.controller_binary")
	if c.Status != StatusWarn {
		t.Errorf("without Self-HA a difference is a warning: %+v", c)
	}
	in.SelfHA = &SelfHAInput{Resource: "haify-meta", Nodes: []string{"n1", "n2"}}
	c = only(t, checkNodes(in), "nodes.controller_binary")
	if c.Status != StatusFail || c.Fix != "./scripts/deploy-all.sh n1,n2" {
		t.Errorf("under Self-HA it fails: %+v", c)
	}
}

func TestHostsEntryPointingAtOldAddress(t *testing.T) {
	in := cluster()
	in.Probes["n3"].Hosts = []HostsEntry{
		{IP: "127.0.1.1", Names: []string{"host-n3"}},
		{IP: "10.0.0.1", Names: []string{"host-n1"}},
		{IP: "10.0.0.99", Names: []string{"host-n2", "n2"}},
	}
	c := only(t, checkNodes(in), "nodes.hosts_entry")
	if c.Subject != "n3:n2" || !strings.Contains(c.Fix, `s/^10\.0\.0\.99[[:space:]]/10.0.0.2 /`) {
		t.Errorf("got %+v", c)
	}
}

func TestIncompleteProbeIsAnError(t *testing.T) {
	in := cluster()
	in.Probes["n1"].Complete = false
	if c := only(t, checkNodes(in), "nodes.probe_incomplete"); c.Status != StatusError {
		t.Errorf("got %+v", c)
	}
}
