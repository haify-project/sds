package inspect

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)

// cluster is three registered nodes, each answering its probe with an empty,
// healthy view; tests add what they need.
func cluster() *Input {
	in := &Input{
		Now:         t0,
		ProbeStart:  t0,
		ProbeEnd:    t0.Add(2 * time.Second),
		Probes:      map[string]*NodeProbe{},
		ProbeErrors: map[string]string{},
		Alerts:      AlertInput{Enabled: true},
		Backups:     BackupInput{SchedulerEnabled: true, Targets: map[string]bool{}, BaseSnapshots: map[string]bool{}},
	}
	for i, n := range []string{"n1", "n2", "n3"} {
		addr := "10.0.0." + string(rune('1'+i))
		in.Nodes = append(in.Nodes, Node{Name: n, Address: addr, Hostname: "host-" + n})
		in.Probes[n] = &NodeProbe{Complete: true, Hostname: "host-" + n, Now: float64(t0.Unix()) + 1, RootUse: 40,
			Addrs: []string{"127.0.0.1", addr}, DRBDKmod: "9.2.12", DRBDUtils: "9.29.0", Reactor: "1.5.0",
			ReactorUp: "active", LVsOK: true, DRBDOK: true}
	}
	return in
}

// up is a peer device in the steady state.
func up(peerDisk string) []DRBDPeerDevice {
	return []DRBDPeerDevice{{Volume: 0, ReplicationState: "Established", PeerDiskState: peerDisk}}
}

// conn is a connection to peer (by hostname, the way DRBD names it).
func conn(peer string, id int, state, role string, pds []DRBDPeerDevice) DRBDConnection {
	return DRBDConnection{Name: "host-" + peer, PeerNodeID: id, ConnectionState: state, PeerRole: role, PeerDevices: pds}
}

// view sets node's own view of a resource.
func view(in *Input, node, res, role, disk string, conns ...DRBDConnection) {
	p := in.Probes[node]
	p.DRBD = append(p.DRBD, DRBDResource{Name: res, Role: role, Devices: []DRBDDevice{{Volume: 0, DiskState: disk}}, Connections: conns})
}

// healthy2 gives resource res two healthy diskful replicas on n1 (Primary)
// and n2, plus a tiebreaker on n3.
func healthy2(in *Input, res string) {
	in.Resources = append(in.Resources, Resource{Name: res, Diskful: []string{"n1", "n2"}, Tiebreakers: []string{"n3"}})
	view(in, "n1", res, "Primary", "UpToDate", conn("n2", 1, "Connected", "Secondary", up("UpToDate")),
		conn("n3", 2, "Connected", "Secondary", up("Diskless")))
	view(in, "n2", res, "Secondary", "UpToDate", conn("n1", 0, "Connected", "Primary", up("UpToDate")),
		conn("n3", 2, "Connected", "Secondary", up("Diskless")))
	view(in, "n3", res, "Secondary", "Diskless", conn("n1", 0, "Connected", "Primary", up("UpToDate")),
		conn("n2", 1, "Connected", "Secondary", up("UpToDate")))
}

func find(checks []Check, id string) []Check {
	var out []Check
	for _, c := range checks {
		if c.ID == id {
			out = append(out, c)
		}
	}
	return out
}

func only(t *testing.T, checks []Check, id string) Check {
	t.Helper()
	got := find(checks, id)
	if len(got) != 1 {
		t.Fatalf("want exactly one %s, got %d in:\n%s", id, len(got), dump(checks))
	}
	return got[0]
}

func allPass(t *testing.T, checks []Check) {
	t.Helper()
	for _, c := range checks {
		if c.Status != StatusPass {
			t.Fatalf("want only pass lines, got:\n%s", dump(checks))
		}
	}
}

func dump(checks []Check) string {
	var b strings.Builder
	for _, c := range checks {
		b.WriteString(string(c.Status) + " " + c.ID + " " + c.Subject + ": " + c.Message + " | fix: " + c.Fix + "\n")
	}
	return b.String()
}
