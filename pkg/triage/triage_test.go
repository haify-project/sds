package triage

import (
	"strings"
	"testing"
	"time"
)

// node builds a NodeReport with one collector, which is what almost every
// situation below needs.
func node(name string, collector string, lines ...string) NodeReport {
	return NodeReport{
		Node: name, Address: name, Reachable: true,
		Collectors: []CollectorOutput{{Collector: collector, Ok: true, Lines: lines}},
	}
}

func findingByID(r Report, id string) (Finding, bool) {
	for _, f := range r.Findings {
		if f.ID == id {
			return f, true
		}
	}
	return Finding{}, false
}

// ── signatures ──────────────────────────────────────────────────────────

// The point of a signature is that two occurrences of one problem collapse and
// two different problems do not. Both halves matter: over-normalising hides a
// fault behind another one's count, and nothing downstream can tell.
func TestSignatureCollapsesWhatVariesAndKeepsWhatNames(t *testing.T) {
	same := []struct{ a, b string }{
		{
			"2026-09-06T09:15:01+0800 sds-b kernel: drbd sds-meta: IO error at offset 4096",
			"2026-09-06T11:42:57+0800 sds-e kernel: drbd sds-meta: IO error at offset 917504",
		},
		{
			"[Sat Sep  6 09:15:01 2026] drbd sds-meta/0 drbd3: connection lost",
			"[Sat Sep  6 10:01:44 2026] drbd sds-meta/0 drbd3: connection lost",
		},
		{
			"reactor[1234]: could not reach peer 192.168.123.227:7789",
			"reactor[9876]: could not reach peer 192.168.123.212:7789",
		},
	}
	for _, c := range same {
		if Signature(c.a) != Signature(c.b) {
			t.Errorf("two occurrences of one problem did not collapse:\n  %s\n  %s", Signature(c.a), Signature(c.b))
		}
	}

	// A digit that names something is part of the name. Collapsing drbd3 and
	// drbd7 would merge two devices' faults into one finding.
	differ := []struct{ a, b string }{
		{"kernel: drbd3: disk failure", "kernel: drbd7: disk failure"},
		{"mount of ext4 failed", "mount of xfs failed"},
		{"vg0/thinpool is full", "vg1/thinpool is full"},
	}
	for _, c := range differ {
		if Signature(c.a) == Signature(c.b) {
			t.Errorf("two different problems collapsed into %q", Signature(c.a))
		}
	}

	// The hostname is stripped only where there is one. dmesg -T has no host
	// field, and a rule loose enough to strip one there eats the subsystem
	// name — after which every DRBD message and every ext4 message are one
	// signature.
	if got := Signature("[Sat Sep  6 09:15:01 2026] drbd sds-meta: connection lost"); !strings.Contains(got, "drbd") {
		t.Errorf("dmesg subsystem name was eaten as a hostname: %q", got)
	}
	// And where there is one, two nodes reporting one fault are one signature —
	// otherwise every cluster-wide problem arrives split in two.
	a := Signature("2026-09-06T09:15:01+0800 sds-b kernel: drbd sds-meta: IO error")
	b := Signature("2026-09-06T09:15:02+0800 sds-e kernel: drbd sds-meta: IO error")
	if a != b {
		t.Errorf("one fault on two nodes did not collapse:\n  %s\n  %s", a, b)
	}
}

// ── phantom peer ────────────────────────────────────────────────────────

const phantomStatus = `sds-meta node-id:1 role:Primary suspended:no
  volume:0 minor:3 disk:UpToDate quorum:yes
  sds-e node-id:2 connection:Connected role:Secondary
  sds-d node-id:3 connection:Connecting role:Unknown`

func TestPhantomPeerIsAPeerTheRegistryDoesNotHave(t *testing.T) {
	in := Input{
		Window: time.Hour,
		Nodes: []NodeReport{
			node("sds-b", "drbd_status", strings.Split(phantomStatus, "\n")...),
			{Node: "sds-e", Address: "sds-e", Reachable: true},
		},
	}
	f, ok := findingByID(Analyze(in), "phantom-peer")
	if !ok {
		t.Fatal("a Connecting peer that is not a registered node was not reported")
	}
	if f.Severity != SeverityCritical || !f.Known {
		t.Errorf("severity=%s known=%v; a quorum that can never be met is critical and known", f.Severity, f.Known)
	}
	if f.Resource != "sds-meta" {
		t.Errorf("resource = %q, want sds-meta from the un-indented line above the peer", f.Resource)
	}
	var hasForget bool
	for _, a := range f.Advice {
		if strings.Contains(a, "forget-peer sds-meta:sds-d") {
			hasForget = true
		}
	}
	if !hasForget {
		t.Errorf("advice does not name the exact forget-peer to run: %v", f.Advice)
	}
	if f.Caution == "" {
		t.Error("forget-peer is irreversible and the finding carries no caution")
	}
}

// A registered node that is merely down also sits in Connecting. Calling that a
// phantom would tell an operator to forget-peer a node they are about to bring
// back — the one piece of advice here that cannot be undone.
func TestARegisteredNodeThatIsDownIsNotAPhantom(t *testing.T) {
	status := `sds-meta node-id:1 role:Primary suspended:no
  sds-e node-id:2 connection:Connecting role:Unknown`
	in := Input{
		Window: time.Hour,
		Nodes: []NodeReport{
			node("sds-b", "drbd_status", strings.Split(status, "\n")...),
			{Node: "sds-e", Address: "sds-e", Reachable: true},
		},
	}
	if f, ok := findingByID(Analyze(in), "phantom-peer"); ok {
		t.Fatalf("a registered node in Connecting was reported as a phantom: %s", f.Title)
	}
}

// An unregistered peer that is Connected is a registry that has fallen behind,
// not a quorum fault: the vote is being cast.
func TestAConnectedUnregisteredPeerIsNotAPhantom(t *testing.T) {
	status := `sds-meta node-id:1 role:Primary suspended:no
  node-c node-id:4 connection:Connected role:Secondary`
	in := Input{
		Window: time.Hour,
		Nodes:  []NodeReport{node("sds-b", "drbd_status", strings.Split(status, "\n")...)},
	}
	if _, ok := findingByID(Analyze(in), "phantom-peer"); ok {
		t.Fatal("a Connected peer was reported as a phantom quorum vote")
	}
}

// ── thin pool ───────────────────────────────────────────────────────────

// The metadata dimension is the one that surprises people: the pool refuses
// writes while its data column still shows room. Reporting it as "the pool is
// full" would send an operator to extend the wrong volume.
func TestThinPoolReportsWhichDimensionIsFull(t *testing.T) {
	lvs := []string{
		"  LV       VG   LSize  Data%  Meta%",
		"  thinpool vg0  80.00g 42.10  97.80",
	}
	r := Analyze(Input{Window: time.Hour, Nodes: []NodeReport{node("sds-b", "storage", lvs...)}})
	if _, ok := findingByID(r, "thin-pool-data"); ok {
		t.Error("data space is 42% full and was reported as a problem")
	}
	f, ok := findingByID(r, "thin-pool-metadata")
	if !ok {
		t.Fatal("metadata at 97.8% was not reported")
	}
	if f.Severity != SeverityError {
		t.Errorf("severity = %s, want error at 95-99%%", f.Severity)
	}
	joined := strings.Join(f.Advice, "\n")
	if !strings.Contains(joined, "--poolmetadatasize") {
		t.Errorf("metadata advice does not extend the metadata volume:\n%s", joined)
	}
	if strings.Contains(joined, "lvextend -L ") {
		t.Errorf("metadata advice tells the operator to extend data space:\n%s", joined)
	}
}

func TestAHealthyPoolIsNotAFinding(t *testing.T) {
	lvs := []string{
		"  LV       VG   LSize  Data%  Meta%",
		"  thinpool vg0  80.00g 12.00  3.40",
		"  data     vg0  10.00g              ",
	}
	r := Analyze(Input{Window: time.Hour, Nodes: []NodeReport{node("sds-b", "storage", lvs...)}})
	for _, f := range r.Findings {
		if strings.HasPrefix(f.ID, "thin-pool") {
			t.Fatalf("a pool at 12%%/3.4%% was reported: %s", f.Title)
		}
	}
}

// ── unplanned failover ──────────────────────────────────────────────────

// The pairing is the whole finding. A failover event alone cannot say whether
// a node died or somebody typed systemctl restart, and stating the second
// without the line would be a guess about who is at fault.
func TestUnplannedFailoverNeedsBothHalves(t *testing.T) {
	at := time.Date(2026, 9, 5, 12, 30, 0, 0, time.UTC)
	fo := Event{
		Type: "resource.failover", Severity: "warning", Status: "info",
		Resource: "sds-meta", Node: "sds-b", Message: "Primary moved from sds-b to sds-e", At: at,
	}

	onlyEvent := Input{Window: time.Hour, Now: at, Events: []Event{fo}}
	if _, ok := findingByID(Analyze(onlyEvent), "unplanned-failover"); ok {
		t.Fatal("a failover with no restart line was blamed on a restart")
	}

	withRestart := onlyEvent
	withRestart.Nodes = []NodeReport{node("sds-b", "promoter_journal",
		"2026-09-05T12:30:01+0000 sds-b systemd[1]: Stopping drbd-services@sds\\x2dmeta.target...")}
	f, ok := findingByID(Analyze(withRestart), "unplanned-failover")
	if !ok {
		t.Fatal("a failover paired with the target being stopped was not identified")
	}
	if !f.Known || f.Resource != "sds-meta" {
		t.Errorf("known=%v resource=%q, want true and sds-meta", f.Known, f.Resource)
	}
	if !strings.Contains(strings.Join(f.Advice, "\n"), "ha evict sds-meta") {
		t.Errorf("advice does not name the supported way to move the role: %v", f.Advice)
	}
}

// A restart hours away from the failover is a different event. Without the
// window this matcher would blame every failover on the last restart in the
// journal, however old.
func TestARestartFarFromTheFailoverDoesNotExplainIt(t *testing.T) {
	at := time.Date(2026, 9, 5, 12, 30, 0, 0, time.UTC)
	in := Input{
		Window: 6 * time.Hour, Now: at,
		Events: []Event{{Type: "resource.failover", Resource: "sds-meta", Severity: "warning", At: at}},
		Nodes: []NodeReport{node("sds-b", "promoter_journal",
			"2026-09-05T08:00:00+0000 sds-b systemd[1]: Stopping drbd-services@sds\\x2dmeta.target...")},
	}
	if _, ok := findingByID(Analyze(in), "unplanned-failover"); ok {
		t.Fatal("a restart four hours earlier was used to explain the failover")
	}
}

// ── what must not be reported ───────────────────────────────────────────

// A resolved event is the record of something that ended. Reported as current,
// a cluster that recovered an hour ago reads exactly like one still down.
func TestResolvedEventsAreNotCurrentProblems(t *testing.T) {
	r := Analyze(Input{Window: time.Hour, Events: []Event{
		{Type: "resource.degraded", Severity: "critical", Status: "resolved",
			Resource: "sds-meta", Message: "replica back UpToDate"},
	}})
	if len(r.Findings) != 0 {
		t.Fatalf("a resolved event was reported as a problem: %+v", r.Findings)
	}
}

// Nothing found and nothing read are the two ways to get an empty list, and
// they mean opposite things. A caller that cannot tell them apart will report
// a cluster nobody could reach as healthy.
func TestEmptyAndUnreadAreDifferentAnswers(t *testing.T) {
	if Analyze(Input{Window: time.Hour}).Healthy {
		t.Error("an analysis that read nothing reported the cluster healthy")
	}
	quiet := Analyze(Input{
		Window: time.Hour,
		Logs:   []LogEntry{{Level: "info", Message: "poll complete"}},
	})
	if !quiet.Healthy {
		t.Error("an analysis that read lines and found nothing did not report healthy")
	}
}

// ── grouping and ordering ───────────────────────────────────────────────

// Four hundred occurrences of one error are one finding with a count. The
// count is itself information — twice is a blip, four hundred times is a loop.
func TestRepeatedErrorsBecomeOneFindingWithACount(t *testing.T) {
	var lines []string
	for i := range 40 {
		lines = append(lines, "2026-09-06T09:15:0"+string(rune('0'+i%10))+
			"+0000 sds-b kernel: drbd sds-meta: I/O error on backing device, sector "+
			string(rune('0'+i%10)))
	}
	r := Analyze(Input{Window: time.Hour, Nodes: []NodeReport{node("sds-b", "drbd_kernel", lines...)}})
	if len(r.Findings) != 1 {
		t.Fatalf("40 occurrences of one error became %d findings", len(r.Findings))
	}
	f := r.Findings[0]
	if f.Count != 40 {
		t.Errorf("count = %d, want 40", f.Count)
	}
	if len(f.Evidence) > maxEvidencePerFind {
		t.Errorf("%d evidence lines kept; the cap is %d", len(f.Evidence), maxEvidencePerFind)
	}
	if f.Evidence[0].Repeat != 40 {
		t.Errorf("the first evidence line does not say it stands for 40: %+v", f.Evidence[0])
	}
}

// A known cause outranks an equally severe pile of lines, because it is the
// one a reader can act on — and two runs over one input must agree, or a
// report cannot be compared with the one from ten minutes ago.
func TestKnownCausesRankFirstAndOrderIsStable(t *testing.T) {
	in := Input{
		Window: time.Hour,
		Nodes: []NodeReport{
			node("sds-b", "drbd_kernel",
				"kernel: drbd sds-meta: Split-Brain detected, dropping connection!",
				"kernel: drbd sds-meta: I/O error on backing device",
				"kernel: drbd sds-meta: I/O error on backing device",
			),
		},
	}
	first := Analyze(in)
	if len(first.Findings) < 2 {
		t.Fatalf("want a known finding and a grouped one, got %d", len(first.Findings))
	}
	if first.Findings[0].ID != "split-brain" {
		t.Errorf("first finding is %q; the one with a cause should lead", first.Findings[0].ID)
	}
	second := Analyze(in)
	for i := range first.Findings {
		if first.Findings[i].ID != second.Findings[i].ID {
			t.Fatalf("two runs over one input disagreed at %d: %q vs %q",
				i, first.Findings[i].ID, second.Findings[i].ID)
		}
	}
}

// A known mode owns its lines. Grouping them again reports one problem twice —
// once with a cause and once without — and the second copy reads as a second
// fault.
func TestAKnownModesLinesAreNotAlsoGrouped(t *testing.T) {
	r := Analyze(Input{Window: time.Hour, Nodes: []NodeReport{
		node("sds-b", "drbd_kernel", "kernel: drbd sds-meta: Split-Brain detected, dropping connection!"),
	}})
	if len(r.Findings) != 1 {
		t.Fatalf("one split-brain line produced %d findings: %+v", len(r.Findings), r.Findings)
	}
}

// ── what was read ───────────────────────────────────────────────────────

// A collector that failed everywhere is a gap in the evidence. A report built
// over a gap has to say so, or it reads as complete.
func TestACollectorThatRanNowhereIsReportedAsAGap(t *testing.T) {
	in := Input{
		Window: time.Hour,
		Nodes: []NodeReport{
			{Node: "sds-b", Reachable: true, Collectors: []CollectorOutput{
				{Collector: "drbd_status", Ok: true, Lines: []string{"sds-meta node-id:1 role:Primary"}},
				{Collector: "storage", Ok: false, Error: "lvs: not found"},
			}},
			{Node: "sds-e", Reachable: true, Collectors: []CollectorOutput{
				{Collector: "drbd_status", Ok: true},
				{Collector: "storage", Ok: false, Error: "lvs: not found"},
			}},
		},
	}
	r := Analyze(in)
	if len(r.Scanned.FailedCollectors) != 1 || r.Scanned.FailedCollectors[0] != "storage" {
		t.Errorf("failed collectors = %v, want [storage]", r.Scanned.FailedCollectors)
	}
}

// One node missing drbd-reactor is normal on a DR-only replica. Calling that a
// gap would put a permanent false warning on every report this cluster makes.
func TestACollectorThatRanSomewhereIsNotAGap(t *testing.T) {
	in := Input{
		Window: time.Hour,
		Nodes: []NodeReport{
			{Node: "sds-b", Reachable: true, Collectors: []CollectorOutput{
				{Collector: "reactor_journal", Ok: true, Lines: []string{"started"}}}},
			{Node: "node-c", Reachable: true, Collectors: []CollectorOutput{
				{Collector: "reactor_journal", Ok: false, Error: "unit not found"}}},
		},
	}
	if got := Analyze(in).Scanned.FailedCollectors; len(got) != 0 {
		t.Errorf("failed collectors = %v, want none: it ran on sds-b", got)
	}
}

func TestUnreachableNodeIsItsOwnFinding(t *testing.T) {
	in := Input{Window: time.Hour, Nodes: []NodeReport{
		{Node: "sds-e", Reachable: false, Error: "dial tcp: i/o timeout"},
	}}
	r := Analyze(in)
	f, ok := findingByID(r, "node-unreachable")
	if !ok {
		t.Fatal("an unreachable node was not reported")
	}
	if !strings.Contains(f.Title, "sds-e") {
		t.Errorf("title does not name the node: %q", f.Title)
	}
	if len(r.Scanned.Unreachable) != 1 {
		t.Errorf("scanned.unreachable = %v, want [sds-e]", r.Scanned.Unreachable)
	}
}
