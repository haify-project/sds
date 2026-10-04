package inspect

import (
	"strings"
	"testing"
)

func TestResourcesHealthyIsOnePassLine(t *testing.T) {
	in := cluster()
	healthy2(in, "data")
	checks := checkResources(in)
	allPass(t, checks)
	if len(checks) != 1 {
		t.Fatalf("want one aggregated pass line, got %d", len(checks))
	}
}

// The iSCSI gateway recorded as started with both replicas Outdated and no
// Primary for two hours.
func TestGatewayWithNoPrimaryAndNoUpToDateReplicaFails(t *testing.T) {
	in := cluster()
	in.Resources = []Resource{{Name: "blk", Diskful: []string{"n1", "n2"}, Tiebreakers: []string{"n3"}}}
	in.Gateways = []Gateway{{Resource: "blk", Type: "iscsi", Status: "started"}}
	view(in, "n1", "blk", "Secondary", "Outdated", conn("n2", 1, "Connected", "Secondary", up("Outdated")))
	view(in, "n2", "blk", "Secondary", "Outdated", conn("n1", 0, "Connected", "Secondary", up("Outdated")))
	for _, n := range []string{"n1", "n2"} {
		in.Probes[n].ReactorConf = []string{"sds-iscsi-blk.toml"}
	}

	c := only(t, checkGateways(in), "gateway.no_primary")
	if c.Status != StatusFail || c.Subject != "blk" {
		t.Fatalf("got %+v", c)
	}
	if !strings.Contains(c.Fix, "drbdadm primary --force blk") || !strings.Contains(c.Fix, "drbdadm secondary blk") {
		t.Errorf("fix should force one replica UpToDate and hand it back to the promoter: %q", c.Fix)
	}
	if len(c.Evidence) != 2 || !strings.Contains(c.Evidence[0], "Outdated") {
		t.Errorf("evidence should list each replica's disk: %v", c.Evidence)
	}
	// The Outdated replicas are failures in their own right.
	if got := find(checkResources(in), "resource.replica_state"); len(got) != 2 {
		t.Errorf("want both Outdated replicas flagged, got:\n%s", dump(got))
	}
}

func TestGatewayNoPrimaryWithUpToDateReplicaRestartsTheGateway(t *testing.T) {
	in := cluster()
	in.Resources = []Resource{{Name: "nfs1", Diskful: []string{"n1", "n2"}}}
	in.Gateways = []Gateway{{Resource: "nfs1", Type: "nfs", Status: "configured"}}
	view(in, "n1", "nfs1", "Secondary", "UpToDate", conn("n2", 1, "Connected", "Secondary", up("UpToDate")))
	view(in, "n2", "nfs1", "Secondary", "UpToDate", conn("n1", 0, "Connected", "Secondary", up("UpToDate")))
	c := only(t, checkGateways(in), "gateway.no_primary")
	if c.Fix != "sds gateway start --resource nfs1" {
		t.Errorf("fix = %q", c.Fix)
	}
}

func TestStoppedGatewayNeedsNoPrimary(t *testing.T) {
	in := cluster()
	in.Resources = []Resource{{Name: "nfs1", Diskful: []string{"n1", "n2"}}}
	in.Gateways = []Gateway{{Resource: "nfs1", Type: "nfs", Status: "stopped"}}
	view(in, "n1", "nfs1", "Secondary", "UpToDate", conn("n2", 1, "Connected", "Secondary", up("UpToDate")))
	view(in, "n2", "nfs1", "Secondary", "UpToDate", conn("n1", 0, "Connected", "Secondary", up("UpToDate")))
	allPass(t, checkGateways(in))
}

// One side WFBitMapS, the other Established: only the node holding the stuck
// half sees it, which is why every node is probed.
func TestAsymmetricStuckHandshakeFails(t *testing.T) {
	in := cluster()
	in.Resources = []Resource{{Name: "vm", Diskful: []string{"n1", "n2"}}}
	view(in, "n1", "vm", "Primary", "UpToDate", conn("n2", 1, "Connected", "Secondary",
		[]DRBDPeerDevice{{ReplicationState: "WFBitMapS", PeerDiskState: "Outdated"}}))
	view(in, "n2", "vm", "Secondary", "Outdated", conn("n1", 0, "Connected", "Primary", up("UpToDate")))

	checks := checkResources(in)
	c := only(t, checks, "resource.stuck_handshake")
	if c.Subject != "vm@n1->n2" || c.Status != StatusFail {
		t.Fatalf("got %+v", c)
	}
	want := "ssh 10.0.0.1 sudo drbdadm disconnect vm:host-n2 && ssh 10.0.0.1 sudo drbdadm connect vm:host-n2"
	if c.Fix != want {
		t.Errorf("fix = %q, want %q", c.Fix, want)
	}
	if got := only(t, checks, "resource.replica_state"); got.Subject != "vm@n2" {
		t.Errorf("the Outdated replica should be named: %+v", got)
	}
}

func TestStandAloneAndDisconnected(t *testing.T) {
	in := cluster()
	in.Resources = []Resource{{Name: "meta", Diskful: []string{"n1", "n2", "n3"}}}
	view(in, "n1", "meta", "Primary", "UpToDate", conn("n2", 1, "StandAlone", "", nil), conn("n3", 2, "Connecting", "", nil))
	view(in, "n2", "meta", "Secondary", "UpToDate", conn("n1", 0, "StandAlone", "", nil))
	view(in, "n3", "meta", "Secondary", "UpToDate")
	checks := checkResources(in)
	if got := find(checks, "resource.standalone"); len(got) != 2 {
		t.Fatalf("want StandAlone flagged from both sides:\n%s", dump(checks))
	}
	sa := find(checks, "resource.standalone")[0]
	if sa.Fix != "ssh 10.0.0.1 sudo drbdsetup connect meta 1" || sa.Runbook == "" {
		t.Errorf("fix should reconnect the named peer: %+v", sa)
	}
	if c := only(t, checks, "resource.disconnected"); c.Subject != "meta->n3" || c.Status != StatusFail {
		t.Errorf("got %+v", c)
	}
}

func TestResyncInProgressIsAWarning(t *testing.T) {
	in := cluster()
	in.Resources = []Resource{{Name: "vm", Diskful: []string{"n1", "n2"}}}
	done := 42.5
	view(in, "n1", "vm", "Primary", "UpToDate", conn("n2", 1, "Connected", "Secondary",
		[]DRBDPeerDevice{{ReplicationState: "SyncSource", PeerDiskState: "Inconsistent", PercentResyncDone: &done}}))
	view(in, "n2", "vm", "Secondary", "Inconsistent", conn("n1", 0, "Connected", "Primary",
		[]DRBDPeerDevice{{ReplicationState: "SyncTarget", PeerDiskState: "UpToDate", PercentResyncDone: &done}}))
	c := only(t, checkResources(in), "resource.resyncing")
	if c.Status != StatusWarn || !strings.Contains(c.Message, "42.5%") {
		t.Errorf("got %+v", c)
	}
}

func TestResourceNotUpAndDisklessDiskful(t *testing.T) {
	in := cluster()
	in.Resources = []Resource{{Name: "vm", Diskful: []string{"n1", "n2", "n3"}}}
	view(in, "n1", "vm", "Primary", "UpToDate")
	view(in, "n2", "vm", "Secondary", "Diskless")
	checks := checkResources(in)
	if c := only(t, checks, "resource.not_up"); c.Subject != "vm@n3" || c.Fix != "ssh 10.0.0.3 sudo drbdadm up vm" {
		t.Errorf("got %+v", c)
	}
	if c := only(t, checks, "resource.replica_state"); c.Fix != "ssh 10.0.0.2 sudo drbdadm attach vm" {
		t.Errorf("got %+v", c)
	}
}

func TestHAResourceWithoutPrimaryAndRisks(t *testing.T) {
	in := cluster()
	in.Resources = []Resource{{Name: "db", Diskful: []string{"n1", "n2"}, ServedBy: "ha", QuorumRisk: true, FaultDomainRisk: "host=pve1"}}
	view(in, "n1", "db", "Secondary", "UpToDate", conn("n2", 1, "Connected", "Secondary", up("UpToDate")))
	view(in, "n2", "db", "Secondary", "UpToDate", conn("n1", 0, "Connected", "Secondary", up("UpToDate")))
	in.Probes["n1"].ReactorConf = []string{"sds-ha-db.toml"}
	in.Probes["n3"].ReactorConf = []string{"sds-ha-db.toml"}
	checks := checkResources(in)
	if c := only(t, checks, "resource.no_primary"); c.Fix != "ssh 10.0.0.1 sudo systemctl restart drbd-reactor" {
		t.Errorf("got %+v", c)
	}
	if c := only(t, checks, "resource.promoter_missing"); !strings.Contains(c.Fix, "ssh 10.0.0.1 sudo cat /etc/drbd-reactor.d/sds-ha-db.toml | ssh 10.0.0.2") {
		t.Errorf("fix should copy the config from n1 to n2: %q", c.Fix)
	}
	only(t, checks, "resource.quorum_risk")
	only(t, checks, "resource.fault_domain")
}

// A diskless Primary works, over the network; a promoter there is noted, not
// failed.
func TestPromoterOnTiebreakerWarns(t *testing.T) {
	in := cluster()
	healthy2(in, "share")
	in.Gateways = []Gateway{{Resource: "share", Type: "nfs", Status: "started"}}
	for _, n := range []string{"n1", "n2", "n3"} {
		in.Probes[n].ReactorConf = []string{"sds-nfs-share.toml"}
	}
	c := only(t, checkGateways(in), "gateway.promoter_on_diskless")
	if c.Subject != "share@n3" || c.Status != StatusWarn || c.Fix != "sds gateway start --resource share" ||
		!strings.Contains(c.Message, "crosses the network") {
		t.Errorf("got %+v", c)
	}
}

func TestPrimarySeenOnlyThroughPeer(t *testing.T) {
	in := cluster()
	in.Resources = []Resource{{Name: "db", Diskful: []string{"n1", "n2"}, ServedBy: "ha"}}
	delete(in.Probes, "n1")
	in.ProbeErrors["n1"] = "timeout"
	view(in, "n2", "db", "Secondary", "UpToDate", conn("n1", 0, "Connected", "Primary", up("UpToDate")))
	in.Probes["n2"].ReactorConf = []string{"sds-ha-db.toml"}
	if got := find(checkResources(in), "resource.no_primary"); len(got) != 0 {
		t.Errorf("the Primary is visible from n2's connection: %s", dump(got))
	}
}

// The DR node of a WAN resource holds an asynchronous copy that may be behind:
// failover to it is manual, so it should have no promoter, and its lacking one
// is not a missing promoter.
func TestPromoterOnDRNodeWarnsAndItsAbsenceIsFine(t *testing.T) {
	in := cluster()
	in.Resources = []Resource{{Name: "db", Diskful: []string{"n1", "n2", "n3"}, DR: "n3", ServedBy: "ha"}}
	view(in, "n1", "db", "Primary", "UpToDate")
	in.Probes["n1"].ReactorConf = []string{"sds-ha-db.toml"}
	in.Probes["n2"].ReactorConf = []string{"sds-ha-db.toml"}
	for _, c := range checkResources(in) {
		if c.ID == "resource.promoter_missing" || c.ID == "resource.promoter_on_dr" {
			t.Errorf("a DR node without a promoter is how it should be: %+v", c)
		}
	}

	in.Probes["n3"].ReactorConf = []string{"sds-ha-db.toml"}
	c := only(t, checkResources(in), "resource.promoter_on_dr")
	if c.Subject != "db@n3" || c.Status != StatusWarn || c.Fix != "sds resource repair db" {
		t.Errorf("got %+v", c)
	}
}
