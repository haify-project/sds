package client

import (
	"context"
	"fmt"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

// AppCreateRequest asks for a database application. Resource defaults to
// Name and Port to the engine's own.
type AppCreateRequest struct {
	Name      string
	Engine    string // postgres, mysql or redis
	Resource  string
	ServiceIP string // CIDR
	Port      uint32
	Vector    bool // postgres only: pgvector
}

// CreateApp creates a database application. The response carries the
// generated password, which is returned by this call only.
func (c *HaifyClient) CreateApp(ctx context.Context, req AppCreateRequest) (*haifypb.CreateAppResponse, error) {
	resp, err := c.client.CreateApp(ctx, &haifypb.CreateAppRequest{
		Name:      req.Name,
		Engine:    req.Engine,
		Resource:  req.Resource,
		ServiceIp: req.ServiceIP,
		Port:      req.Port,
		Vector:    req.Vector,
	})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}

// ListApps lists the database applications.
func (c *HaifyClient) ListApps(ctx context.Context) ([]*haifypb.AppInfo, error) {
	resp, err := c.client.ListApps(ctx, &haifypb.ListAppsRequest{})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Apps, nil
}

// GetAppStatus reports where an app runs and whether it answers there.
func (c *HaifyClient) GetAppStatus(ctx context.Context, name string) (*haifypb.GetAppStatusResponse, error) {
	resp, err := c.client.GetAppStatus(ctx, &haifypb.GetAppStatusRequest{Name: name})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}

// DeleteApp removes an app; with deleteData its resource is deleted too.
func (c *HaifyClient) DeleteApp(ctx context.Context, name string, deleteData bool) (string, error) {
	resp, err := c.client.DeleteApp(ctx, &haifypb.DeleteAppRequest{Name: name, DeleteData: deleteData})
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("%s", resp.Message)
	}
	return resp.Message, nil
}

// FailoverApp moves an app to another replica.
func (c *HaifyClient) FailoverApp(ctx context.Context, name string) (*haifypb.FailoverAppResponse, error) {
	resp, err := c.client.FailoverApp(ctx, &haifypb.FailoverAppRequest{Name: name})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}

// SnapshotApp takes a resource snapshot of an app with the database frozen.
func (c *HaifyClient) SnapshotApp(ctx context.Context, name, snapshot string) (*haifypb.SnapshotAppResponse, error) {
	resp, err := c.client.SnapshotApp(ctx, &haifypb.SnapshotAppRequest{Name: name, Snapshot: snapshot})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}
