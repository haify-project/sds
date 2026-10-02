package inspect

import (
	"strings"
	"testing"
)

// Two powered-off PVE hosts attached as diskless clients, seen from three
// nodes: one warning per client, not six failures.
func TestUnreachableDisklessClientsAreOneWarningEach(t *testing.T) {
	in := cluster()
	in.Resources = []Resource{{Name: "pve-9001-0", Diskful: []string{"n1", "n2"}, Tiebreakers: []string{"n3"},
		Clients: []string{"pve-a", "pve-b"}}}
	down := func(peer string, id int) DRBDConnection {
		return DRBDConnection{Name: peer, PeerNodeID: id, ConnectionState: "Connecting"}
	}
	view(in, "n1", "pve-9001-0", "Secondary", "UpToDate", conn("n2", 1, "Connected", "Secondary", up("UpToDate")), down("pve-a", 3), down("pve-b", 4))
	view(in, "n2", "pve-9001-0", "Secondary", "UpToDate", conn("n1", 0, "Connected", "Secondary", up("UpToDate")), down("pve-a", 3), down("pve-b", 4))
	view(in, "n3", "pve-9001-0", "Secondary", "Diskless", down("pve-a", 3), down("pve-b", 4))

	got := find(checkResources(in), "resource.disconnected")
	if len(got) != 2 {
		t.Fatalf("want one item per client:\n%s", dump(got))
	}
	c := got[0]
	if c.Subject != "pve-9001-0->pve-a" || c.Status != StatusWarn || len(c.Evidence) != 4 {
		t.Errorf("got %+v", c)
	}
	if c.Fix != "start pve-a, or detach it: sds resource diskless detach pve-9001-0 pve-a" {
		t.Errorf("fix = %q", c.Fix)
	}
	if !strings.Contains(c.Message, "n1, n2, n3") {
		t.Errorf("message should name every node that cannot reach it: %s", c.Message)
	}
}

func TestUnreachableTiebreakerWarns(t *testing.T) {
	in := cluster()
	in.Resources = []Resource{{Name: "vm", Diskful: []string{"n1", "n2"}, Tiebreakers: []string{"n3"}}}
	view(in, "n1", "vm", "Primary", "UpToDate", conn("n2", 1, "Connected", "Secondary", up("UpToDate")), conn("n3", 2, "Connecting", "", nil))
	view(in, "n2", "vm", "Secondary", "UpToDate", conn("n1", 0, "Connected", "Primary", up("UpToDate")), conn("n3", 2, "Connecting", "", nil))
	c := only(t, checkResources(in), "resource.disconnected")
	if c.Status != StatusWarn || !strings.Contains(c.Fix, "sds ha set-tiebreaker vm") {
		t.Errorf("got %+v", c)
	}
}

// One host under every resource is one finding, not one per resource.
func TestFaultDomainAggregates(t *testing.T) {
	in := cluster()
	for _, r := range []string{"a", "b", "c"} {
		in.Resources = append(in.Resources, Resource{Name: r, FaultDomainRisk: "host=dell"})
	}
	in.Resources = append(in.Resources, Resource{Name: "d", FaultDomainRisk: "host=hp"})
	got := find(checkResources(in), "resource.fault_domain")
	if len(got) != 2 {
		t.Fatalf("want one per domain:\n%s", dump(got))
	}
	if got[0].Subject != "host=dell" || got[0].Evidence[0] != "a, b, c" || !strings.HasPrefix(got[0].Message, "3 resources") {
		t.Errorf("got %+v", got[0])
	}
}
