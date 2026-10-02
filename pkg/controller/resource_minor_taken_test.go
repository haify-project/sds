package controller

import "testing"

// The ten-hour outage: the queen's own volume was still up as a diskless
// client on the node it was re-attaching to, and drbdsetup's line for it was
// read as somebody else's.
func TestAResourcesOwnMinorFromDrbdsetupIsNotASquatter(t *testing.T) {
	out := "/etc/drbd.d/pvc_q.res:        device minor 35;\n" +
		"resource \"pvc_other\" {\n    volume 0 {\n        device minor 30;\n" +
		"resource \"pvc_q\" {\n    volume 0 {\n        device minor 35;\n"
	if m, owner, taken := minorTaken(out, "pvc_q", map[int]bool{35: true}); taken {
		t.Fatalf("own minor %d reported as taken by %s", m, owner)
	}
}

func TestAMinorHeldByAnotherResourceIsNamed(t *testing.T) {
	out := "resource \"pvc_other\" {\n    volume 0 {\n        device minor 35;\n"
	m, owner, taken := minorTaken(out, "pvc_q", map[int]bool{35: true})
	if !taken || m != 35 || owner != "pvc_other" {
		t.Fatalf("got %d %q %v", m, owner, taken)
	}
}

func TestAMinorInAnotherResFileIsNamed(t *testing.T) {
	out := "/etc/drbd.d/pvc_other.res:        device minor 35;\n"
	_, owner, taken := minorTaken(out, "pvc_q", map[int]bool{35: true})
	if !taken || owner != "pvc_other" {
		t.Fatalf("got %q %v", owner, taken)
	}
}

func TestAnUnownedMinorIsStillAConflict(t *testing.T) {
	// A device line with no resource before it and no file: a stale node.
	out := "        device minor 35;\n"
	_, owner, taken := minorTaken(out, "pvc_q", map[int]bool{35: true})
	if !taken || owner != "another resource or a stale device node" {
		t.Fatalf("got %q %v", owner, taken)
	}
}

func TestMinorsNotWantedAreIgnored(t *testing.T) {
	out := "resource \"pvc_other\" {\n    volume 0 {\n        device minor 30;\n"
	if _, _, taken := minorTaken(out, "pvc_q", map[int]bool{35: true}); taken {
		t.Fatal("a minor nobody asked about was a conflict")
	}
}

func TestUnquotedResourceNamesAreRead(t *testing.T) {
	if name, ok := drbdsetupResource("resource pvc_q {"); !ok || name != "pvc_q" {
		t.Fatalf("%q %v", name, ok)
	}
	if name, ok := drbdsetupResource(`resource "pvc_q" {`); !ok || name != "pvc_q" {
		t.Fatalf("%q %v", name, ok)
	}
}
