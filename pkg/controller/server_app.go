package controller

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/haify-project/haify/pkg/apptemplate"
)

// Database application RPCs (app*.go). Like the other operation RPCs they
// report failure in the response; only a request that fails validation
// before anything is done is a gRPC error (InvalidArgument).

func (s *Server) CreateApp(ctx context.Context, req *haifypb.CreateAppRequest) (*haifypb.CreateAppResponse, error) {
	spec := apptemplate.Spec{Name: req.Name, Engine: apptemplate.Engine(req.Engine), Resource: req.Resource,
		ServiceIP: req.ServiceIp, Port: int(req.Port), Vector: req.Vector}
	created, err := s.ctrl.appManager().Create(ctx, spec)
	if err != nil {
		if errors.Is(err, apptemplate.ErrInvalid) {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		return &haifypb.CreateAppResponse{Success: false, Message: err.Error()}, nil
	}
	info := appInfo(created.App)
	msg := fmt.Sprintf("app %s (%s) created on resource %s; drbd-reactor starts it on a replica and raises %s; "+
		"connect with: %s", created.App.Name, created.App.Engine, created.App.Resource, created.App.ServiceIP, info.Connection)
	if created.Reused {
		msg = fmt.Sprintf("app %s (%s) created on resource %s, which already held its data: the data and the "+
			"credentials in %s (root, on the Primary) are kept", created.App.Name, created.App.Engine,
			created.App.Resource, info.CredentialsFile)
	}
	return &haifypb.CreateAppResponse{
		Success:     true,
		Message:     msg,
		App:         info,
		Password:    created.Password,
		PrimaryNode: created.Primary,
		DataReused:  created.Reused,
	}, nil
}

func (s *Server) ListApps(ctx context.Context, _ *haifypb.ListAppsRequest) (*haifypb.ListAppsResponse, error) {
	apps, err := s.ctrl.appManager().List(ctx)
	if err != nil {
		return &haifypb.ListAppsResponse{Success: false, Message: err.Error()}, nil
	}
	resp := &haifypb.ListAppsResponse{Success: true, Apps: make([]*haifypb.AppInfo, 0, len(apps))}
	for _, a := range apps {
		resp.Apps = append(resp.Apps, appInfo(a))
	}
	return resp, nil
}

func (s *Server) GetAppStatus(ctx context.Context, req *haifypb.GetAppStatusRequest) (*haifypb.GetAppStatusResponse, error) {
	st, err := s.ctrl.appManager().Status(ctx, req.Name)
	if err != nil {
		return &haifypb.GetAppStatusResponse{Success: false, Message: err.Error()}, nil
	}
	msg := fmt.Sprintf("app %s is %s", req.Name, st.State)
	if st.Primary != "" {
		msg += " on " + st.Primary
	}
	return &haifypb.GetAppStatusResponse{
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

func (s *Server) DeleteApp(ctx context.Context, req *haifypb.DeleteAppRequest) (*haifypb.DeleteAppResponse, error) {
	msg, err := s.ctrl.appManager().Delete(ctx, req.Name, req.DeleteData)
	if err != nil {
		return &haifypb.DeleteAppResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.DeleteAppResponse{Success: true, Message: msg}, nil
}

func (s *Server) FailoverApp(ctx context.Context, req *haifypb.FailoverAppRequest) (*haifypb.FailoverAppResponse, error) {
	from, to, err := s.ctrl.appManager().Failover(ctx, req.Name)
	if err != nil {
		return &haifypb.FailoverAppResponse{Success: false, Message: err.Error(), FromNode: from}, nil
	}
	msg := fmt.Sprintf("app %s moved off %s", req.Name, from)
	if to != "" {
		msg = fmt.Sprintf("app %s moved from %s to %s", req.Name, from, to)
	}
	return &haifypb.FailoverAppResponse{Success: true, Message: msg, FromNode: from, ToNode: to}, nil
}

func (s *Server) SnapshotApp(ctx context.Context, req *haifypb.SnapshotAppRequest) (*haifypb.SnapshotAppResponse, error) {
	frozen, err := s.ctrl.appManager().Snapshot(ctx, req.Name, req.Snapshot)
	if err != nil {
		return &haifypb.SnapshotAppResponse{Success: false, Message: err.Error(), Frozen: frozen}, nil
	}
	msg := fmt.Sprintf("snapshot %s of app %s taken on every replica with the database frozen", req.Snapshot, req.Name)
	if !frozen {
		msg = fmt.Sprintf("snapshot %s of app %s taken on every replica; the app was not running, so there was "+
			"nothing to freeze", req.Snapshot, req.Name)
	}
	return &haifypb.SnapshotAppResponse{Success: true, Message: msg, Frozen: frozen}, nil
}
