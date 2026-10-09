package controller

import (
	"context"
	"strings"
	"testing"

	pb "github.com/haify-project/haify/api/proto/v1"
)

func peerConn(name, role string, oos uint64, repl, disk string) drbdConnection {
	return drbdConnection{Name: name, ConnectionState: "Connected", PeerRole: role,
		PeerDevices: []drbdPeerDevice{{ReplicationState: repl, PeerDiskState: disk, OutOfSyncKiB: oos}}}
}

func TestVerifiable(t *testing.T) {
	cases := []struct {
		conn, repl, disk string
		ok               bool
	}{
		{"Connected", "Established", "UpToDate", true},
		{"Connecting", "", "", false},
		{"Connected", "Established", "Diskless", false},
		{"Connected", "SyncSource", "Inconsistent", false},
		{"Connected", "Ahead", "Outdated", false},
	}
	for _, c := range cases {
		got := verifiable(c.conn, []drbdPeerDevice{{ReplicationState: c.repl, PeerDiskState: c.disk}})
		if (got == "") != c.ok {
			t.Errorf("%s/%s/%s: got %q", c.conn, c.repl, c.disk, got)
		}
	}
}

// Verify cannot tell which copy is right, so resync must only ever copy from
// a node that can be trusted, and never over the data a service is reading.
func TestResyncOutOfSyncRefusesWithoutAuthority(t *testing.T) {
	ctx := context.Background()
	rm := &ResourceManager{}
	noop := func(string, ...any) {}

	two := &drbdsetupStatus{Name: "r", Role: "Secondary"}
	two.Connections = append(two.Connections, peerConn("b", "Secondary", 12, "Established", "UpToDate"),
		peerConn("tb", "Secondary", 0, "Established", "Diskless"))
	if err := rm.resyncOutOfSync(ctx, "r", "a", two, noop); err == nil || !strings.Contains(err.Error(), "no telling") {
		t.Fatalf("a Secondary with no agreeing copy must not win: %v", err)
	}

	intoPrimary := &drbdsetupStatus{Name: "r", Role: "Secondary"}
	intoPrimary.Connections = append(intoPrimary.Connections, peerConn("b", "Primary", 12, "Established", "UpToDate"),
		peerConn("c", "Secondary", 0, "Established", "UpToDate"))
	if err := rm.resyncOutOfSync(ctx, "r", "a", intoPrimary, noop); err == nil || !strings.Contains(err.Error(), "is Primary") {
		t.Fatalf("the Primary's data must never be overwritten: %v", err)
	}

	clean := &drbdsetupStatus{Name: "r", Role: "Primary"}
	clean.Connections = append(clean.Connections, peerConn("b", "Secondary", 0, "Established", "UpToDate"))
	if err := rm.resyncOutOfSync(ctx, "r", "a", clean, noop); err == nil || !strings.Contains(err.Error(), "nothing to resync") {
		t.Fatalf("got %v", err)
	}
}

func TestVerifyPeersSkipsDiskless(t *testing.T) {
	st := &drbdsetupStatus{Name: "r"}
	st.Connections = append(st.Connections, peerConn("b", "Secondary", 12, "Established", "UpToDate"),
		peerConn("tb", "Secondary", 0, "Established", "Diskless"),
		peerConn("c", "Secondary", 0, "VerifyS", "UpToDate"))
	peers := verifyPeers(st, nil)
	if len(peers) != 2 || peers[0].OutOfSyncKib != 12 || peers[1].State != "verifying" {
		t.Fatalf("got %+v", peers)
	}
	if !verifying(st) {
		t.Fatal("VerifyS is a running verify")
	}
}

func TestVerifyMessageSeparatesNewFindsFromOldMarks(t *testing.T) {
	// Marks that were there before the verify are not its findings.
	old := []*pb.VerifyPeer{{Node: "b", OutOfSyncKib: 1000, BaselineKnown: true}}
	msg, marked := verifyMessage("r", "a", old)
	if !strings.Contains(msg, "found no new difference") || len(marked) != 1 {
		t.Fatalf("%q", msg)
	}
	found := []*pb.VerifyPeer{{Node: "b", OutOfSyncKib: 1012, FoundKib: 12, BaselineKnown: true}}
	if msg, _ := verifyMessage("r", "a", found); !strings.Contains(msg, "found 12 KiB that differ") || !strings.Contains(msg, "1000 KiB were marked before") {
		t.Fatalf("%q", msg)
	}
	unknown := []*pb.VerifyPeer{{Node: "b", OutOfSyncKib: 50}}
	if msg, _ := verifyMessage("r", "a", unknown); !strings.Contains(msg, "cannot say how much") {
		t.Fatalf("%q", msg)
	}
	if msg, marked := verifyMessage("r", "a", []*pb.VerifyPeer{{Node: "b"}}); marked != nil || !strings.Contains(msg, "same data") {
		t.Fatalf("%q", msg)
	}
}

func TestVerifyRemembersMarksPerPeer(t *testing.T) {
	rm := &ResourceManager{}
	st := &drbdsetupStatus{}
	st.Connections = append(st.Connections, peerConn("b", "Secondary", 700, "Established", "UpToDate"))
	rm.rememberMarks("r", st)
	if v, ok := rm.marksBefore("r", "b"); !ok || v != 700 {
		t.Fatalf("got %d %v", v, ok)
	}
	if _, ok := rm.marksBefore("r", "c"); ok {
		t.Fatal("a peer it never saw has no baseline")
	}
	peers := verifyPeers(func() *drbdsetupStatus {
		s := &drbdsetupStatus{}
		s.Connections = append(s.Connections, peerConn("b", "Secondary", 712, "Established", "UpToDate"))
		return s
	}(), func(p string) (uint64, bool) { return rm.marksBefore("r", p) })
	if peers[0].FoundKib != 12 || !peers[0].BaselineKnown {
		t.Fatalf("%+v", peers[0])
	}
}
