package controller

import (
	"strings"
	"testing"
)

const sampleRes = `resource data {

    options {
        auto-promote no;
        quorum majority;
        on-no-quorum io-error;
    }

    net {
        protocol C;
        rr-conflict retry-connect;
    }

    volume 0 {
        device    minor 0;
        disk      /dev/sds_vg0/data_data;
        meta-disk internal;
    }

    volume 1 {
        device    minor 1001;
        disk      /dev/sds_vg0/data_state;
        meta-disk internal;
    }

    on orange1 {
        address   192.168.123.214:7000;
        node-id   0;
    }

    connection-mesh {
        hosts orange1 orange2 orange3;
    }
}
`

func TestApplyDrbdOptions(t *testing.T) {
	out, err := applyDrbdOptions(sampleRes, map[string]string{
		"on-no-quorum":        "suspend-io",                      // replace existing in options
		"net/max-buffers":     "8000",                            // add to existing net block
		"disk/on-io-error":    "detach",                          // create disk block inside volume 0
		"handlers/fence-peer": "/usr/lib/drbd/crm-fence-peer.sh", // create new block
	})
	if err != nil {
		t.Fatalf("applyDrbdOptions: %v", err)
	}

	checks := []string{
		"on-no-quorum suspend-io;",                    // replaced
		"max-buffers 8000;",                           // added to net
		"on-io-error detach;",                         // disk option
		"fence-peer /usr/lib/drbd/crm-fence-peer.sh;", // new handlers block
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("expected output to contain %q\n---\n%s", c, out)
		}
	}

	// The old value must be gone (replaced, not duplicated).
	if strings.Contains(out, "on-no-quorum io-error;") {
		t.Errorf("old on-no-quorum value should have been replaced")
	}
	if strings.Count(out, "on-no-quorum") != 1 {
		t.Errorf("on-no-quorum should appear exactly once, got %d", strings.Count(out, "on-no-quorum"))
	}

	// Volumes, on-section and mesh must be preserved untouched.
	for _, keep := range []string{
		"volume 1 {", "/dev/sds_vg0/data_state", "on orange1 {",
		"192.168.123.214:7000;", "connection-mesh {",
	} {
		if !strings.Contains(out, keep) {
			t.Errorf("expected preserved content %q to remain", keep)
		}
	}

	// disk options must land inside volume 0, not at the resource top level.
	v0 := out[strings.Index(out, "volume 0 {"):strings.Index(out, "volume 1 {")]
	if !strings.Contains(v0, "on-io-error detach;") {
		t.Errorf("disk option should be inside volume 0 block")
	}

	// The result must still be brace-balanced.
	if strings.Count(out, "{") != strings.Count(out, "}") {
		t.Errorf("unbalanced braces after edit")
	}
}

func TestApplyDrbdOptionsEmptyConfig(t *testing.T) {
	if _, err := applyDrbdOptions("", map[string]string{"x": "y"}); err == nil {
		t.Errorf("expected error for empty config")
	}
}
