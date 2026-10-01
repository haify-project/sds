package database

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ==================== Gateway Tests ====================

func TestGatewayCRUD(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create
	gateway := &Gateway{
		Name:       "nfs-gateway",
		Resource:   "data",
		Type:       GatewayTypeNFS,
		Config:     map[string]interface{}{"export_path": "/export/data"},
		Status:     "active",
		ActiveNode: "node1",
	}

	err := db.SaveGateway(ctx, gateway)
	require.NoError(t, err)
	assert.NotZero(t, gateway.ID)

	// Read
	retrieved, err := db.GetGateway(ctx, gateway.Name)
	require.NoError(t, err)
	assert.Equal(t, gateway.Name, retrieved.Name)
	assert.Equal(t, gateway.Resource, retrieved.Resource)
	assert.Equal(t, gateway.Type, retrieved.Type)
	assert.Equal(t, gateway.Status, retrieved.Status)

	// Update
	gateway.Status = "failed"
	gateway.ActiveNode = "node2"
	err = db.SaveGateway(ctx, gateway)
	require.NoError(t, err)

	retrieved, err = db.GetGateway(ctx, gateway.Name)
	require.NoError(t, err)
	assert.Equal(t, "failed", retrieved.Status)
	assert.Equal(t, "node2", retrieved.ActiveNode)

	// List
	gateways, err := db.ListGateways(ctx)
	require.NoError(t, err)
	assert.Len(t, gateways, 1)

	// Delete
	err = db.DeleteGateway(ctx, gateway.Name)
	require.NoError(t, err)

	_, err = db.GetGateway(ctx, gateway.Name)
	assert.Error(t, err)
}

func TestGatewayTypes(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()

	types := []GatewayType{GatewayTypeNFS, GatewayTypeISCSI, GatewayTypeNVMEOF}
	for _, gwType := range types {
		gateway := &Gateway{
			Name:     string(gwType) + "-gateway",
			Resource: "data",
			Type:     gwType,
			Status:   "active",
		}

		err := db.SaveGateway(ctx, gateway)
		require.NoError(t, err)
	}

	gateways, err := db.ListGateways(ctx)
	require.NoError(t, err)
	assert.Len(t, gateways, 3)
}

func TestGetGatewayByResource(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()
	gateway := &Gateway{
		Name:       "data-nfs",
		Resource:   "data",
		Type:       GatewayTypeNFS,
		Status:     "configured",
		ActiveNode: "node1",
	}

	err := db.SaveGateway(ctx, gateway)
	require.NoError(t, err)

	retrieved, err := db.GetGatewayByResource(ctx, "data")
	require.NoError(t, err)
	assert.Equal(t, gateway.Name, retrieved.Name)
	assert.Equal(t, gateway.Resource, retrieved.Resource)
}

func TestDeleteGatewayByResource(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()
	first := &Gateway{Name: "data-nfs", Resource: "data", Type: GatewayTypeNFS}
	second := &Gateway{Name: "logs-nfs", Resource: "logs", Type: GatewayTypeNFS}

	require.NoError(t, db.SaveGateway(ctx, first))
	require.NoError(t, db.SaveGateway(ctx, second))

	require.NoError(t, db.DeleteGatewayByResource(ctx, "data"))

	_, err := db.GetGateway(ctx, "data-nfs")
	assert.Error(t, err)

	retrieved, err := db.GetGateway(ctx, "logs-nfs")
	require.NoError(t, err)
	assert.Equal(t, "logs", retrieved.Resource)
}
