package controller

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	sdspb "github.com/haify-project/sds/api/proto/v1"
	"github.com/haify-project/sds/pkg/apptemplate"
)

// Database application RPCs (app*.go). Like the other operation RPCs they
// report failure in the response; only a request that fails validation
// before anything is done is a gRPC error (InvalidArgument).

func (s *Server) CreateApp(ctx context.Context, req *sdspb.CreateAppRequest) (*sdspb.CreateAppResponse, error) {
	spec := apptemplate.Spec{Name: req.Name, Engine: apptemplate.Engine(req.Engine), Resource: req.Resource,
		ServiceIP: req.ServiceIp, Port: int(req.Port), Vector: req.Vector}
	created, err := s.ctrl.appManager().Create(ctx, spec)
	if err != nil {
		if errors.Is(err, apptemplate.ErrInvalid) {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		return &sdspb.CreateAppResponse{Success: false, Message: err.Error()}, nil
	}
	info := appInfo(created.App)
	msg := fmt.Sprintf("app %s (%s) created on resource %s; drbd-reactor starts it on a replica and raises %s; "+
		"connect with: %s", created.App.Name, created.App.Engine, created.App.Resource, created.App.ServiceIP, info.Connection)
	if created.Reused {
		msg = fmt.Sprintf("app %s (%s) created on resource %s, which already held its data: the data and the "+
			"credentials in %s (root, on the Primary) are kept", created.App.Name, created.App.Engine,
			created.App.Resource, info.CredentialsFile)
	}
	return &sdspb.CreateAppResponse{
		Success:     true,
		Message:     msg,
		App:         info,
		Password:    created.Password,
		PrimaryNode: created.Primary,
		DataReused:  created.Reused,
	}, nil
}

func (s *Server) ListApps(ctx context.Context, _ *sdspb.ListAppsRequest) (*sdspb.ListAppsResponse, error) {
	apps, err := s.ctrl.appManager().List(ctx)
	if err != nil {
		return &sdspb.ListAppsResponse{Success: false, Message: err.Error()}, nil
	}
	resp := &sdspb.ListAppsResponse{Success: true, Apps: make([]*sdspb.AppInfo, 0, len(apps))}
	for _, a := range apps {
		resp.Apps = append(resp.Apps, appInfo(a))
	}
	return resp, nil
}

func (s *Server) GetAppStatus(ctx context.Context, req *sdspb.GetAppStatusRequest) (*sdspb.GetAppStatusResponse, error) {
	st, err := s.ctrl.appManager().Status(ctx, req.Name)
	if err != nil {
		return &sdspb.GetAppStatusResponse{Success: false, Message: err.Error()}, nil
	}
	msg := fmt.Sprintf("app %s is %s", req.Name, st.State)
	if st.Primary != "" {
		msg += " on " + st.Primary
	}
	return &sdspb.GetAppStatusResponse{
		Success:      true,
		Message:      msg,
		App:          appInfo(st.App),
		PrimaryNode:  st.Primary,
		ServiceState: st.ServiceState,
		Healthy:      st.Healthy,
		State:        st.State,
		Nodes:        st.Nodes,
	}, nil
}

func (s *Server) DeleteApp(ctx context.Context, req *sdspb.DeleteAppRequest) (*sdspb.DeleteAppResponse, error) {
	msg, err := s.ctrl.appManager().Delete(ctx, req.Name, req.DeleteData)
	if err != nil {
		return &sdspb.DeleteAppResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.DeleteAppResponse{Success: true, Message: msg}, nil
}

func (s *Server) FailoverApp(ctx context.Context, req *sdspb.FailoverAppRequest) (*sdspb.FailoverAppResponse, error) {
	from, to, err := s.ctrl.appManager().Failover(ctx, req.Name)
	if err != nil {
		return &sdspb.FailoverAppResponse{Success: false, Message: err.Error(), FromNode: from}, nil
	}
	msg := fmt.Sprintf("app %s moved off %s", req.Name, from)
	if to != "" {
		msg = fmt.Sprintf("app %s moved from %s to %s", req.Name, from, to)
	}
	return &sdspb.FailoverAppResponse{Success: true, Message: msg, FromNode: from, ToNode: to}, nil
}

func (s *Server) SnapshotApp(ctx context.Context, req *sdspb.SnapshotAppRequest) (*sdspb.SnapshotAppResponse, error) {
	frozen, err := s.ctrl.appManager().Snapshot(ctx, req.Name, req.Snapshot)
	if err != nil {
		return &sdspb.SnapshotAppResponse{Success: false, Message: err.Error(), Frozen: frozen}, nil
	}
	msg := fmt.Sprintf("snapshot %s of app %s taken on every replica with the database frozen", req.Snapshot, req.Name)
	if !frozen {
		msg = fmt.Sprintf("snapshot %s of app %s taken on every replica; the app was not running, so there was "+
			"nothing to freeze", req.Snapshot, req.Name)
	}
	return &sdspb.SnapshotAppResponse{Success: true, Message: msg, Frozen: frozen}, nil
}
