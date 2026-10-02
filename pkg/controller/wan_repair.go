package controller

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	pb "github.com/haify-project/sds/api/proto/v1"
	"github.com/haify-project/sds/pkg/database"
	"github.com/haify-project/sds/pkg/wanproxy"
)

// RepairWanProxy reconciles a WAN resource's replication tunnels with the
// controller's current view of its nodes.
//
// Leg names used to embed a node's address, so renumbering a node orphaned its
// tunnel: it kept replicating under the old name while everything that
// recomputed the name looked for one that did not exist — status reported a
// phantom outage, and deprovisioning would have missed it entirely. Legs are
// named after node names now, which do not move, but deployments created before
// that still carry address-named legs. This converges them.
//
// Re-provisioning restarts the tunnels, so DryRun exists to show what would
// change first.
func (s *Server) RepairWanProxy(ctx context.Context, req *pb.RepairWanProxyRequest) (*pb.RepairWanProxyResponse, error) {
	name := req.GetName()
	if name == "" {
		return &pb.RepairWanProxyResponse{Success: false, Message: "resource name is required"}, nil
	}
	if s.ctrl.db == nil {
		return &pb.RepairWanProxyResponse{Success: false, Message: "database not available"}, nil
	}

	dbRes, err := s.ctrl.db.GetResource(ctx, name)
	if err != nil || dbRes == nil {
		return &pb.RepairWanProxyResponse{Success: false, Message: fmt.Sprintf("resource %q not found", name)}, nil
	}
	if !dbRes.WANMode {
		return &pb.RepairWanProxyResponse{
			Success: false,
			Message: fmt.Sprintf("resource %q is not a WAN resource", name),
		}, nil
	}
	return s.repairWanLegs(ctx, dbRes, req.GetDryRun(), false), nil
}

// repairWanLegs converges a WAN resource's tunnels on its database record.
// skipCheck provisions without probing that the DR endpoint answers — for an
// endpoint the operator knows is not reachable yet.
func (s *Server) repairWanLegs(ctx context.Context, dbRes *database.Resource, dryRun, skipCheck bool) *pb.RepairWanProxyResponse {
	name := dbRes.Name
	rm := s.ctrl.resources
	multi := rm.wanMultiSpecFor(dbRes)
	deploy := rm.wanproxyDeployClient()

	resp := &pb.RepairWanProxyResponse{Success: true}
	for _, leg := range multi.Legs() {
		resp.ExpectedLegs = append(resp.ExpectedLegs,
			fmt.Sprintf("%s -> %s", leg.PrimaryNodeAddr, leg.Resource))
	}

	stale, err := wanproxy.FindStaleLegs(ctx, deploy, multi)
	if err != nil {
		return &pb.RepairWanProxyResponse{Success: false, Message: err.Error()}
	}
	for _, st := range stale {
		resp.RemovedLegs = append(resp.RemovedLegs, fmt.Sprintf("%s: %s", st.Host, st.LegID))
	}

	if dryRun {
		resp.AlreadyConsistent = len(stale) == 0
		resp.Message = fmt.Sprintf("dry run: %d leg(s) expected, %d stale instance(s) would be removed",
			len(resp.ExpectedLegs), len(stale))
		return resp
	}

	multi.SkipReachabilityCheck = skipCheck
	multi.BinaryFor = rm.wanproxyBinaryResolver(ctx,
		append(append([]string{}, multi.PrimaryNodeAddrs...), multi.DRNodeAddr))
	s.ctrl.logger.Info("Repairing WAN proxy legs",
		zap.String("resource", name),
		zap.Strings("expected", resp.ExpectedLegs),
		zap.Int("stale", len(stale)))

	// Stale legs go first, before their replacements are provisioned.
	//
	// The tempting order is the other one — stand the new tunnel up, then sweep
	// the old — so there is never a moment without a path to the DR. It does not
	// work for the case this exists for. Renaming a leg keeps its ports and
	// changes only its name, so the orphan is still holding the very loopback
	// port its replacement needs; provisioning first just fails to bind, and the
	// repair leaves both the old and new units broken.
	//
	// The gap this opens is one provisioning round on an asynchronous DR leg,
	// which DRBD closes by reconnecting and catching up. That is the cheaper
	// failure by a wide margin.
	if err := wanproxy.RemoveStaleLegs(ctx, deploy, stale); err != nil {
		return &pb.RepairWanProxyResponse{
			Success: false,
			Message: fmt.Sprintf("remove stale legs for %s: %v", name, err),
		}
	}
	if err := wanproxy.ProvisionMulti(ctx, deploy, multi); err != nil {
		return &pb.RepairWanProxyResponse{
			Success: false,
			Message: fmt.Sprintf("provision legs for %s: %v", name, err),
		}
	}

	resp.AlreadyConsistent = len(stale) == 0
	resp.Message = fmt.Sprintf("repaired %d leg(s); removed %d stale instance(s)",
		len(resp.ExpectedLegs), len(stale))
	return resp
}
