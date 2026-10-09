package inspect

import "testing"

func selfHA(in *Input) {
	in.SelfHA = &SelfHAInput{Resource: "haify-meta", Nodes: []string{"n1", "n2", "n3"}}
	in.Resources = append(in.Resources, Resource{Name: "haify-meta", Diskful: []string{"n1", "n2", "n3"}, ServedBy: "self-ha"})
	view(in, "n1", "haify-meta", "Primary", "UpToDate", conn("n2", 1, "Connected", "Secondary", up("UpToDate")),
		conn("n3", 2, "Connected", "Secondary", up("UpToDate")))
	view(in, "n2", "haify-meta", "Secondary", "UpToDate")
	view(in, "n3", "haify-meta", "Secondary", "UpToDate")
	for _, n := range []string{"n1", "n2", "n3"} {
		p := in.Probes[n]
		p.ReactorConf = []string{selfHAPromoter}
		p.CtlBin, p.CtlSHA = "/opt/haify/bin/haify-controller", "abc"
		p.CtlActive = "inactive"
	}
	in.Probes["n1"].CtlActive = "active"
}

func TestSelfHAHealthy(t *testing.T) {
	in := cluster()
	selfHA(in)
	allPass(t, checkSelfHA(in))
}

func TestSelfHAFindings(t *testing.T) {
	in := cluster()
	selfHA(in)
	in.Probes["n2"].DRBD[0].Devices[0].DiskState = "Outdated"
	in.Probes["n3"].DRBD[0].Devices[0].DiskState = "Inconsistent"
	in.Probes["n3"].ReactorConf = []string{selfHAPromoter + ".disabled"}
	in.Probes["n2"].CtlActive = "active"
	in.Probes["n3"].CtlBin = ""
	checks := checkSelfHA(in)
	if c := only(t, checks, "selfha.meta_replicas"); c.Status != StatusFail {
		t.Errorf("got %+v", c)
	}
	only(t, checks, "selfha.promoter_missing")
	if c := only(t, checks, "selfha.controller_active"); c.Fix != "ssh 10.0.0.2 sudo systemctl stop haify-controller" {
		t.Errorf("the controller not on the haify-meta Primary is the one to stop: %+v", c)
	}
	if c := only(t, checks, "selfha.controller_binary"); c.Subject != "n3" {
		t.Errorf("got %+v", c)
	}
}

func TestSelfHAControllerOffThePrimary(t *testing.T) {
	in := cluster()
	selfHA(in)
	in.Probes["n1"].CtlActive = "inactive"
	in.Probes["n3"].CtlActive = "active"
	if c := only(t, checkSelfHA(in), "selfha.controller_active"); c.Subject != "n3" {
		t.Errorf("got %+v", c)
	}
}

func TestSelfHADisabled(t *testing.T) {
	in := cluster()
	if c := only(t, checkSelfHA(in), "selfha.disabled"); c.Status != StatusPass {
		t.Errorf("got %+v", c)
	}
}
