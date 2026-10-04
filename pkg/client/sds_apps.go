package client

import (
	"context"
	"fmt"

	sdspb "github.com/haify-project/sds/api/proto/v1"
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
func (c *SDSClient) CreateApp(ctx context.Context, req AppCreateRequest) (*sdspb.CreateAppResponse, error) {
	resp, err := c.client.CreateApp(ctx, &sdspb.CreateAppRequest{
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
func (c *SDSClient) ListApps(ctx context.Context) ([]*sdspb.AppInfo, error) {
	resp, err := c.client.ListApps(ctx, &sdspb.ListAppsRequest{})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Apps, nil
}

// GetAppStatus reports where an app runs and whether it answers there.
func (c *SDSClient) GetAppStatus(ctx context.Context, name string) (*sdspb.GetAppStatusResponse, error) {
	resp, err := c.client.GetAppStatus(ctx, &sdspb.GetAppStatusRequest{Name: name})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}

// DeleteApp removes an app; with deleteData its resource is deleted too.
func (c *SDSClient) DeleteApp(ctx context.Context, name string, deleteData bool) (string, error) {
	resp, err := c.client.DeleteApp(ctx, &sdspb.DeleteAppRequest{Name: name, DeleteData: deleteData})
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("%s", resp.Message)
	}
	return resp.Message, nil
}

// FailoverApp moves an app to another replica.
func (c *SDSClient) FailoverApp(ctx context.Context, name string) (*sdspb.FailoverAppResponse, error) {
	resp, err := c.client.FailoverApp(ctx, &sdspb.FailoverAppRequest{Name: name})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}

// SnapshotApp takes a resource snapshot of an app with the database frozen.
func (c *SDSClient) SnapshotApp(ctx context.Context, name, snapshot string) (*sdspb.SnapshotAppResponse, error) {
	resp, err := c.client.SnapshotApp(ctx, &sdspb.SnapshotAppRequest{Name: name, Snapshot: snapshot})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp, nil
}
