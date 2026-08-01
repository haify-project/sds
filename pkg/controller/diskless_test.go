package controller

import (
	"strings"
	"testing"
)

// A representative two-node LAN resource as generateDrbdConfig would emit it:
// resource-level volume block, two diskful `on` stanzas, no connection-mesh
// (DRBD only needs one for >2 nodes).
const twoNodeConfig = `resource data {

    net {
        protocol C;
        rr-conflict retry-connect;
    }

    volume 0 {
        device    minor 100;
        disk      /dev/vg0/data_vol0;
        meta-disk internal;
    }

    on node1 {
        address   10.0.0.1:7000;
        node-id   0;
    }

    on node2 {
        address   10.0.0.2:7000;
        node-id   1;
    }
}
`

// A three-node resource: two diskful plus one existing diskless tiebreaker,
// carrying a connection-mesh listing all three.
const threeNodeConfig = `resource data {

    net {
        protocol C;
    }

    volume 0 {
        device    minor 100;
        disk      /dev/vg0/data_vol0;
        meta-disk internal;
    }

    on node1 {
        address   10.0.0.1:7000;
        node-id   0;
    }

    on node2 {
        address   10.0.0.2:7000;
        node-id   1;
    }

    on tb1 {
        address   10.0.0.3:7000;
        node-id   2;
        volume 0 {
            device    minor 100;
            disk      none;
        }
    }

    connection-mesh {
        hosts node1 node2 tb1;
    }
}
`

func TestParseOnBlocks(t *testing.T) {
	blocks := parseOnBlocks(threeNodeConfig)
	if len(blocks) != 3 {
		t.Fatalf("want 3 on-blocks, got %d: %+v", len(blocks), blocks)
	}
	want := []onBlock{{name: "node1", nodeID: 0}, {name: "node2", nodeID: 1}, {name: "tb1", nodeID: 2}}
	for i, b := range blocks {
		if b != want[i] {
			t.Errorf("block %d = %+v, want %+v", i, b, want[i])
		}
	}
}

func TestParseOnBlocks_IgnoresNestedVolume(t *testing.T) {
	// The diskless tiebreaker's nested `volume 0 {` must not be mistaken for an
	// on-block, and its inner braces must not swallow the following stanza.
	blocks := parseOnBlocks(threeNodeConfig)
	if got := onHostNames(blocks); strings.Join(got, ",") != "node1,node2,tb1" {
		t.Fatalf("host order wrong: %v", got)
	}
}

func TestAddDisklessClientBlock_TwoToThree(t *testing.T) {
	out, err := addDisklessClientBlock(twoNodeConfig, "client1", "10.0.0.9", 7000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// New client stanza present with the next free node-id (2) and disk none.
	if !strings.Contains(out, "on client1 {") {
		t.Error("client on-block missing")
	}
	if !strings.Contains(out, "node-id   2;") {
		t.Errorf("expected node-id 2 for new client, got:\n%s", out)
	}
	if !strings.Contains(out, "address   10.0.0.9:7000;") {
		t.Error("client address missing")
	}
	if !strings.Contains(out, "disk      none;") {
		t.Error("client volume should override disk none")
	}
	// Crossing 2 -> 3 nodes must introduce a full mesh of all three hosts.
	if !strings.Contains(out, "connection-mesh {") {
		t.Error("connection-mesh should be added when going to 3 nodes")
	}
	if !strings.Contains(out, "hosts node1 node2 client1;") {
		t.Errorf("mesh should list all three hosts, got:\n%s", out)
	}
	// The client's minor must match the resource-level volume's minor.
	if !strings.Contains(out, "device    minor 100;") {
		t.Error("client volume minor should mirror resource-level minor 100")
	}
	assertBalancedBraces(t, out)
}

func TestAddDisklessClientBlock_ThreeToFour(t *testing.T) {
	out, err := addDisklessClientBlock(threeNodeConfig, "client1", "10.0.0.9", 7000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "node-id   3;") {
		t.Errorf("expected node-id 3 for 4th node, got:\n%s", out)
	}
	// The pre-existing mesh must be replaced by one listing all four hosts,
	// exactly once.
	if n := strings.Count(out, "connection-mesh {"); n != 1 {
		t.Errorf("want exactly 1 connection-mesh, got %d", n)
	}
	if !strings.Contains(out, "hosts node1 node2 tb1 client1;") {
		t.Errorf("mesh should list all four hosts, got:\n%s", out)
	}
	assertBalancedBraces(t, out)
}

func TestAddDisklessClientBlock_Idempotent(t *testing.T) {
	_, err := addDisklessClientBlock(threeNodeConfig, "node1", "10.0.0.1", 7000)
	if err != errDisklessAlreadyPresent {
		t.Fatalf("re-adding existing host should return errDisklessAlreadyPresent, got %v", err)
	}
}

func TestRemoveDisklessClientBlock_FourToThree(t *testing.T) {
	four, err := addDisklessClientBlock(threeNodeConfig, "client1", "10.0.0.9", 7000)
	if err != nil {
		t.Fatalf("setup add failed: %v", err)
	}
	out, err := removeDisklessClientBlock(four, "client1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, "on client1 {") {
		t.Error("client on-block should be gone")
	}
	// Still 3 nodes, so a mesh remains — now without client1.
	if !strings.Contains(out, "hosts node1 node2 tb1;") {
		t.Errorf("mesh should list remaining three hosts, got:\n%s", out)
	}
	assertBalancedBraces(t, out)
}

func TestRemoveDisklessClientBlock_ThreeToTwoDropsMesh(t *testing.T) {
	out, err := removeDisklessClientBlock(threeNodeConfig, "tb1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, "on tb1 {") {
		t.Error("tb1 on-block should be gone")
	}
	// Two nodes left: DRBD needs no mesh, and a stale 3-host mesh would be wrong.
	if strings.Contains(out, "connection-mesh") {
		t.Errorf("mesh should be dropped at 2 nodes, got:\n%s", out)
	}
	if len(parseOnBlocks(out)) != 2 {
		t.Errorf("want 2 remaining on-blocks, got %d", len(parseOnBlocks(out)))
	}
	assertBalancedBraces(t, out)
}

func TestRemoveDisklessClientBlock_NotPresent(t *testing.T) {
	_, err := removeDisklessClientBlock(twoNodeConfig, "ghost")
	if err != errDisklessNotPresent {
		t.Fatalf("removing absent host should return errDisklessNotPresent, got %v", err)
	}
}

func TestRemoveDisklessClientBlock_ExactHostMatch(t *testing.T) {
	// A prefix like "node1" must not match "node10"; removing node1 must leave
	// node10 intact.
	cfg := strings.Replace(threeNodeConfig, "on tb1 {", "on node10 {", 1)
	out, err := removeDisklessClientBlock(cfg, "node1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "on node10 {") {
		t.Error("node10 should not be removed when removing node1")
	}
	if strings.Contains(out, "on node1 {") {
		t.Error("node1 should be removed")
	}
}

func TestAddRemoveRoundTrip(t *testing.T) {
	added, err := addDisklessClientBlock(twoNodeConfig, "client1", "10.0.0.9", 7000)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	out, err := removeDisklessClientBlock(added, "client1")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	// Round trip returns to two diskful nodes with no mesh.
	if got := onHostNames(parseOnBlocks(out)); strings.Join(got, ",") != "node1,node2" {
		t.Errorf("round trip host set = %v, want [node1 node2]", got)
	}
	if strings.Contains(out, "connection-mesh") {
		t.Error("round trip should leave no mesh at 2 nodes")
	}
}

// assertBalancedBraces guards against config surgery that drops or adds a brace.
func assertBalancedBraces(t *testing.T, cfg string) {
	t.Helper()
	if o, c := strings.Count(cfg, "{"), strings.Count(cfg, "}"); o != c {
		t.Errorf("unbalanced braces: %d open vs %d close in:\n%s", o, c, cfg)
	}
}
