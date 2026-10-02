package gateway

import (
	"strings"
	"testing"
)

// Two initiators must reach the OCF agent as one allowed_initiators value.
// drbd-reactor splits the start line with shell-word rules, so the list is
// quoted; reading the line back must give the same list.
func TestISCSITargetLineQuotesAMultiEntryAllowList(t *testing.T) {
	line := buildISCSITargetLine("iqn.t", "10.0.0.1:3260", "", "",
		[]string{"iqn.a:1", "iqn.b:2"}, "lio-t")
	if !strings.Contains(line, `allowed_initiators=\"iqn.a:1 iqn.b:2\"`) {
		t.Fatalf("list not quoted for drbd-reactor: %s", line)
	}
	params, ok := parseISCSITargetLine(line)
	if !ok {
		t.Fatal("line not recognised")
	}
	got := parseAllowedList(params["allowed_initiators"])
	if strings.Join(got, ",") != "iqn.a:1,iqn.b:2" {
		t.Fatalf("read back %v", got)
	}
	if params["implementation"] != "lio-t" {
		t.Fatalf("parameter after the list lost: %v", params)
	}
}

func TestSingleEntryAndUnquotedListsStillParse(t *testing.T) {
	line := buildISCSITargetLine("iqn.t", "p", "", "", []string{"iqn.a:1"}, "lio-t")
	if strings.Contains(line, `\"`) {
		t.Fatalf("a single initiator needs no quotes: %s", line)
	}
	// Lines written before quoting existed keep working.
	old := `        "ocf:heartbeat:iSCSITarget target iqn=iqn.t portals=p allowed_initiators=iqn.a:1 iqn.b:2 implementation=lio-t",`
	params, _ := parseISCSITargetLine(old)
	if got := parseAllowedList(params["allowed_initiators"]); len(got) != 2 {
		t.Fatalf("old unquoted line read as %v", got)
	}
}

func TestNVMeSubsystemLineQuotesAMultiEntryHostList(t *testing.T) {
	line := buildNVMeSubsystemLine("nqn.x", []string{"nqn.h1", "nqn.h2"}, "abc")
	if !strings.Contains(line, `allowed_initiators=\"nqn.h1 nqn.h2\"`) {
		t.Fatalf("host list not quoted: %s", line)
	}
}
