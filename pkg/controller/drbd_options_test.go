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

	// disk options must land inside every volume, not at the resource top
	// level, exactly as generateDrbdConfig writes them at create time.
	v0 := out[strings.Index(out, "volume 0 {"):strings.Index(out, "volume 1 {")]
	v1 := out[strings.Index(out, "volume 1 {"):strings.Index(out, "on orange1 {")]
	for name, block := range map[string]string{"volume 0": v0, "volume 1": v1} {
		if !strings.Contains(block, "on-io-error detach;") {
			t.Errorf("disk option should be inside the %s block\n---\n%s", name, out)
		}
	}
	if n := strings.Count(out, "on-io-error detach;"); n != 2 {
		t.Errorf("disk option should appear once per volume, got %d", n)
	}

	// The result must still be brace-balanced.
	if strings.Count(out, "{") != strings.Count(out, "}") {
		t.Errorf("unbalanced braces after edit")
	}
}

// A multi-volume config with a diskless node: set-options must reach every
// resource-level volume, replace an existing disk option in place, and leave
// the tiebreaker's per-node `disk none` overrides alone.
func TestApplyDrbdOptionsDiskEveryVolume(t *testing.T) {
	cfg := `resource data {

    volume 0 {
        device    minor 100;
        disk      /dev/vg0/data_vol0;
        meta-disk internal;
        disk {
            on-io-error pass_on;
        }
    }

    volume 1 {
        device    minor 101;
        disk      /dev/vg0/data_vol1;
        meta-disk internal;
    }

    volume 2 {
        device    minor 102;
        disk      /dev/vg0/data_vol2;
        meta-disk internal;
    }

    on tb1 {
        address   10.0.0.3:7000;
        node-id   2;
        volume 0 {
            disk      none;
        }
        volume 1 {
            disk      none;
        }
        volume 2 {
            disk      none;
        }
    }
}
`
	out, err := applyDrbdOptions(cfg, map[string]string{"disk/on-io-error": "detach", "disk/c-plan-ahead": "0"})
	if err != nil {
		t.Fatalf("applyDrbdOptions: %v", err)
	}
	for _, want := range []string{"on-io-error detach;", "c-plan-ahead 0;"} {
		if n := strings.Count(out, want); n != 3 {
			t.Errorf("%q should appear once per volume (3), got %d\n---\n%s", want, n, out)
		}
	}
	if strings.Contains(out, "pass_on") {
		t.Errorf("existing disk option should have been replaced\n---\n%s", out)
	}
	tb := out[strings.Index(out, "on tb1 {"):]
	if strings.Contains(tb, "on-io-error") || strings.Contains(tb, "c-plan-ahead") {
		t.Errorf("per-node volume overrides must not receive disk options\n---\n%s", out)
	}
	if strings.Count(out, "{") != strings.Count(out, "}") {
		t.Errorf("unbalanced braces after edit")
	}
}

func TestApplyDrbdOptionsEmptyConfig(t *testing.T) {
	if _, err := applyDrbdOptions("", map[string]string{"x": "y"}); err == nil {
		t.Errorf("expected error for empty config")
	}
}
