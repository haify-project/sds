package database

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppCRUD(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// An empty database has no apps bucket yet; reads must not fail on it.
	got, err := db.GetApp(ctx, "orders")
	require.NoError(t, err)
	assert.Nil(t, got)
	apps, err := db.ListApps(ctx)
	require.NoError(t, err)
	assert.Empty(t, apps)
	require.NoError(t, db.DeleteApp(ctx, "orders"))

	orders := &App{Name: "orders", Engine: "postgres", Resource: "orders-r", ServiceIP: "10.0.0.50/24", Port: 5432,
		Vector: true, Device: "/dev/drbd/by-res/orders-r/0", Server: "/usr/lib/postgresql/16/bin/postgres",
		Client: "/usr/lib/postgresql/16/bin/psql", UID: 113, GID: 120, Version: "16.4"}
	cache := &App{Name: "cache", Engine: "redis", Resource: "cache", ServiceIP: "10.0.0.51/24", Port: 6379}
	require.NoError(t, db.SaveApp(ctx, orders))
	require.NoError(t, db.SaveApp(ctx, cache))
	assert.False(t, orders.CreatedAt.IsZero())

	got, err = db.GetApp(ctx, "orders")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, orders.Resource, got.Resource)
	assert.Equal(t, orders.Server, got.Server)
	assert.Equal(t, 113, got.UID)
	assert.True(t, got.Vector)

	apps, err = db.ListApps(ctx)
	require.NoError(t, err)
	require.Len(t, apps, 2)
	assert.Equal(t, "cache", apps[0].Name, "listed by name")
	assert.Equal(t, "orders", apps[1].Name)

	byRes, err := db.GetAppByResource(ctx, "orders-r")
	require.NoError(t, err)
	require.NotNil(t, byRes)
	assert.Equal(t, "orders", byRes.Name)
	none, err := db.GetAppByResource(ctx, "other")
	require.NoError(t, err)
	assert.Nil(t, none)

	created := got.CreatedAt
	got.Port = 5433
	require.NoError(t, db.SaveApp(ctx, got))
	again, err := db.GetApp(ctx, "orders")
	require.NoError(t, err)
	assert.Equal(t, 5433, again.Port)
	assert.True(t, again.CreatedAt.Equal(created), "an update keeps the creation time")

	require.NoError(t, db.DeleteApp(ctx, "orders"))
	got, err = db.GetApp(ctx, "orders")
	require.NoError(t, err)
	assert.Nil(t, got)

	assert.Error(t, db.SaveApp(ctx, &App{}), "an app needs a name")
}
