package controller

import (
	"context"
	"strings"
	"testing"

	pb "github.com/haify-project/sds/api/proto/v1"
	"github.com/haify-project/sds/pkg/deployment"
)

// diagTestServer registers the nodes and then forgets that it did.
//
// RegisterNode runs its own health-check commands through the same fake, so
// leaving them in execCalls would make every "how many commands ran" assertion
// below count two more than the collectors — and the point of those assertions
// is the exact number.
func diagTestServer(t *testing.T, dep *fakeDeploymentClient, nodes map[string]string) *Server {
	t.Helper()
	ctrl := newBasicTestController(dep)
	for name, addr := range nodes {
		if _, err := ctrl.nodes.RegisterNode(context.Background(), name, addr); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	dep.execCalls = nil
	return &Server{ctrl: ctrl, resources: ctrl.resources}
}

// The security property this whole design rests on: a request names collectors,
// never commands. If a caller's text could reach the shell, this would be a
// remote command channel wearing a diagnostics label — and it is reachable
// through an MCP tool an assistant drives.
func TestNothingACallerSendsReachesTheShell(t *testing.T) {
	dep := &fakeDeploymentClient{}
	srv := diagTestServer(t, dep, map[string]string{"node1": "10.0.0.1"})

	hostile := []string{
		"; rm -rf /",
		"drbd_status; touch /tmp/pwned",
		"$(id)",
		"`id`",
		"drbd_status\nrm -rf /",
	}
	resp, err := srv.CollectNodeDiagnostics(context.Background(), &pb.CollectNodeDiagnosticsRequest{
		Collectors: hostile,
		Nodes:      []string{"; rm -rf /", "node1"},
	})
	if err != nil {
		t.Fatalf("CollectNodeDiagnostics: %v", err)
	}
	if resp.Success {
		t.Error("a request naming only unknown collectors succeeded")
	}
	if len(dep.execCalls) != 0 {
		t.Fatalf("a command was run for an unknown collector: %+v", dep.execCalls)
	}
	if len(resp.UnknownCollectors) != len(hostile) {
		t.Errorf("unknown collectors = %v, want all %d reported back", resp.UnknownCollectors, len(hostile))
	}

	// And with a real collector name, the commands that run are the table's —
	// none of them carries a byte the caller wrote.
	dep.execCalls = nil
	if _, err := srv.CollectNodeDiagnostics(context.Background(), &pb.CollectNodeDiagnosticsRequest{
		Collectors: []string{"drbd_status"},
		Nodes:      []string{"node1"},
	}); err != nil {
		t.Fatalf("CollectNodeDiagnostics: %v", err)
	}
	if len(dep.execCalls) != 1 {
		t.Fatalf("want 1 exec, got %d", len(dep.execCalls))
	}
	for _, h := range hostile {
		if strings.Contains(dep.execCalls[0].cmd, h) {
			t.Errorf("caller text reached the command: %q", dep.execCalls[0].cmd)
		}
	}
}

// Only registered nodes are collected from. Without this a request could name
// an arbitrary host for the controller to ssh into — and the controller holds
// a key to every node.
func TestOnlyRegisteredNodesAreCollectedFrom(t *testing.T) {
	dep := &fakeDeploymentClient{}
	srv := diagTestServer(t, dep, map[string]string{"node1": "10.0.0.1"})

	resp, err := srv.CollectNodeDiagnostics(context.Background(), &pb.CollectNodeDiagnosticsRequest{
		Collectors: []string{"drbd_status"},
		Nodes:      []string{"192.0.2.99"},
	})
	if err != nil {
		t.Fatalf("CollectNodeDiagnostics: %v", err)
	}
	if resp.Success {
		t.Error("a request for an unregistered host succeeded")
	}
	if len(dep.execCalls) != 0 {
		t.Fatalf("the controller reached out to an unregistered host: %+v", dep.execCalls)
	}
}

// One Exec per collector across every host, not one per pair. On a nine-node
// cluster the difference is nine commands against eighty-one, each with its
// own SSH round trip.
func TestCollectorsFanOutOncePerCollector(t *testing.T) {
	dep := &fakeDeploymentClient{}
	srv := diagTestServer(t, dep, map[string]string{
		"node1": "10.0.0.1", "node2": "10.0.0.2", "node3": "10.0.0.3",
	})

	if _, err := srv.CollectNodeDiagnostics(context.Background(), &pb.CollectNodeDiagnosticsRequest{
		Collectors: []string{"drbd_status", "storage"},
	}); err != nil {
		t.Fatalf("CollectNodeDiagnostics: %v", err)
	}
	if len(dep.execCalls) != 2 {
		t.Fatalf("want 2 execs for 2 collectors over 3 nodes, got %d", len(dep.execCalls))
	}
	if len(dep.execCalls[0].hosts) != 3 {
		t.Errorf("collector went to %d hosts, want all 3", len(dep.execCalls[0].hosts))
	}
}

// The commands ask for one line more than the cap, and that extra line is what
// tells the caller it is looking at a tail. Reporting a tail as the whole
// record is how a diagnosis misses the first error, which is the one that
// matters.
func TestTruncationIsReportedNotGuessed(t *testing.T) {
	var lines []string
	for i := range 10 {
		lines = append(lines, "line "+string(rune('a'+i)))
	}
	dep := &fakeDeploymentClient{
		execFunc: func(_ context.Context, hosts []string, _ string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			return successExecResult(hosts, strings.Join(lines, "\n")), nil
		},
	}
	srv := diagTestServer(t, dep, map[string]string{"node1": "10.0.0.1"})

	resp, err := srv.CollectNodeDiagnostics(context.Background(), &pb.CollectNodeDiagnosticsRequest{
		Collectors: []string{"drbd_status"}, MaxLines: 4,
	})
	if err != nil {
		t.Fatalf("CollectNodeDiagnostics: %v", err)
	}
	got := resp.Nodes[0].Collectors[0]
	if !got.Truncated {
		t.Error("10 lines came back under a cap of 4 without being marked truncated")
	}
	if len(got.Lines) != 4 {
		t.Fatalf("kept %d lines under a cap of 4", len(got.Lines))
	}
	// The tail, not the head: the newest lines are the ones a reader wants and
	// the command already returned only the end of the record.
	if got.Lines[3] != "line j" {
		t.Errorf("kept the head instead of the tail: %v", got.Lines)
	}
	if !strings.Contains(got.Command, "tail -n 5") {
		t.Errorf("command did not ask for cap+1 lines: %q", got.Command)
	}
}

// A collector that exceeds nothing must not claim it did. `truncated` is read
// as "there is more you have not seen".
func TestShortOutputIsNotMarkedTruncated(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(_ context.Context, hosts []string, _ string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			return successExecResult(hosts, "one\ntwo"), nil
		},
	}
	srv := diagTestServer(t, dep, map[string]string{"node1": "10.0.0.1"})
	resp, _ := srv.CollectNodeDiagnostics(context.Background(), &pb.CollectNodeDiagnosticsRequest{
		Collectors: []string{"drbd_status"}, MaxLines: 100,
	})
	if got := resp.Nodes[0].Collectors[0]; got.Truncated {
		t.Errorf("2 lines under a cap of 100 were marked truncated: %v", got.Lines)
	}
}

// An empty collector list means all of them, because a caller investigating a
// symptom does not yet know which record holds the answer.
func TestNoCollectorsNamedMeansEveryCollector(t *testing.T) {
	dep := &fakeDeploymentClient{}
	srv := diagTestServer(t, dep, map[string]string{"node1": "10.0.0.1"})

	resp, err := srv.CollectNodeDiagnostics(context.Background(), &pb.CollectNodeDiagnosticsRequest{})
	if err != nil {
		t.Fatalf("CollectNodeDiagnostics: %v", err)
	}
	if len(dep.execCalls) != len(diagCollectors) {
		t.Errorf("ran %d collectors, the table has %d", len(dep.execCalls), len(diagCollectors))
	}
	if len(resp.AvailableCollectors) != len(diagCollectors) {
		t.Errorf("available_collectors = %v, want the whole table", resp.AvailableCollectors)
	}
}

// The window and the cap are bounded before they reach a command. A caller
// asking for a year of dmesg at two million lines is not malicious, but it is
// an answer nothing can read and a controller that spends minutes producing it.
func TestWindowAndCapAreClamped(t *testing.T) {
	dep := &fakeDeploymentClient{}
	srv := diagTestServer(t, dep, map[string]string{"node1": "10.0.0.1"})

	if _, err := srv.CollectNodeDiagnostics(context.Background(), &pb.CollectNodeDiagnosticsRequest{
		Collectors: []string{"reactor_journal"}, SinceMinutes: 999999, MaxLines: 999999,
	}); err != nil {
		t.Fatalf("CollectNodeDiagnostics: %v", err)
	}
	cmd := dep.execCalls[0].cmd
	if !strings.Contains(cmd, "-10080min") {
		t.Errorf("window not clamped to a week: %q", cmd)
	}
	if !strings.Contains(cmd, "tail -n 2001") {
		t.Errorf("line cap not clamped: %q", cmd)
	}
}

// A node that answers nothing at all is unreachable for this call's purposes,
// whatever the registry last recorded. The distinction matters downstream: an
// unreachable node is itself a finding, while a collector that ran and found
// nothing is a clean result.
func TestANodeThatAnswersNothingIsUnreachable(t *testing.T) {
	dep := &fakeDeploymentClient{}
	srv := diagTestServer(t, dep, map[string]string{"node1": "10.0.0.1"})
	// Registration needs a node that answers; the collection does not get one.
	dep.execFunc = func(_ context.Context, hosts []string, _ string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		res := &deployment.ExecResult{Hosts: map[string]*deployment.HostResult{}}
		for _, h := range hosts {
			res.Hosts[h] = &deployment.HostResult{
				Host: h, Success: false, Error: context.DeadlineExceeded,
			}
		}
		return res, nil
	}
	resp, _ := srv.CollectNodeDiagnostics(context.Background(), &pb.CollectNodeDiagnosticsRequest{
		Collectors: []string{"drbd_status"},
	})
	if resp.Nodes[0].Reachable {
		t.Error("a node whose collector failed with no output was reported reachable")
	}
	if resp.Nodes[0].Error == "" {
		t.Error("an unreachable node carries no reason")
	}
}
