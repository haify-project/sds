package controller

import (
	"context"
	"path/filepath"
	"testing"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/haify-project/haify/pkg/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestApplyResourceProfile_DefaultsAndOverrides(t *testing.T) {
	profile := &database.ResourceProfile{
		Name:        "production",
		Protocol:    "C",
		StorageType: "lvm-thin",
		Pool:        "fast",
		Replicas:    3,
		OnDifferent: []string{"zone", "rack"},
		OnSame:      []string{"region"},
		DRBDOptions: map[string]string{
			"net/max-buffers":  "8000",
			"disk/on-io-error": "detach",
		},
		Labels: map[string]string{
			"env":  "prod",
			"tier": "default",
		},
	}
	req := &haifypb.CreateResourceRequest{
		Name:                "database",
		Protocol:            "A",
		SizeGb:              10,
		ReplicasOnDifferent: []string{"host"},
		DrbdOptions: map[string]string{
			"net/max-buffers": "16000",
		},
		Labels: map[string]string{
			"app":  "postgres",
			"tier": "critical",
		},
	}

	applyResourceProfile(req, profile)

	assert.Equal(t, "A", req.Protocol)
	assert.Equal(t, "lvm-thin", req.StorageType)
	assert.Equal(t, "fast", req.Pool)
	assert.Equal(t, uint32(3), req.Replicas)
	assert.Equal(t, []string{"host"}, req.ReplicasOnDifferent)
	assert.Equal(t, []string{"region"}, req.ReplicasOnSame)
	assert.Equal(t, map[string]string{
		"net/max-buffers":  "16000",
		"disk/on-io-error": "detach",
	}, req.DrbdOptions)
	assert.Equal(t, map[string]string{
		"env": "prod", "tier": "critical", "app": "postgres",
	}, req.Labels)
	assert.Equal(t, "production", req.Profile)
}

func TestServerResourceProfileCRUD(t *testing.T) {
	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "haify.db")}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = db
	server := NewServer(ctrl)
	ctx := context.Background()

	created, err := server.CreateResourceProfile(ctx, &haifypb.CreateResourceProfileRequest{Profile: &haifypb.ResourceProfile{
		Name:        "production",
		Protocol:    "C",
		StorageType: "lvm-thin",
		Pool:        "fast",
		Replicas:    3,
		Labels:      map[string]string{"env": "prod"},
	}})
	require.NoError(t, err)
	require.True(t, created.Success, created.Message)
	assert.Equal(t, "production", created.Profile.Name)

	got, err := server.GetResourceProfile(ctx, &haifypb.GetResourceProfileRequest{Name: "production"})
	require.NoError(t, err)
	require.True(t, got.Success, got.Message)
	assert.Equal(t, uint32(3), got.Profile.Replicas)

	listed, err := server.ListResourceProfiles(ctx, &haifypb.ListResourceProfilesRequest{})
	require.NoError(t, err)
	require.True(t, listed.Success, listed.Message)
	require.Len(t, listed.Profiles, 1)

	deleted, err := server.DeleteResourceProfile(ctx, &haifypb.DeleteResourceProfileRequest{Name: "production"})
	require.NoError(t, err)
	require.True(t, deleted.Success, deleted.Message)
	missing, err := server.GetResourceProfile(ctx, &haifypb.GetResourceProfileRequest{Name: "production"})
	require.NoError(t, err)
	assert.False(t, missing.Success)
}

func TestApplyResourceProfile_FillsEmptyVolumePools(t *testing.T) {
	req := &haifypb.CreateResourceRequest{
		Volumes: []*haifypb.VolumeSpec{
			{SizeGb: 10},
			{SizeGb: 20, Pool: "archive"},
		},
	}

	applyResourceProfile(req, &database.ResourceProfile{Name: "default", Pool: "fast"})

	assert.Equal(t, "fast", req.Volumes[0].Pool)
	assert.Equal(t, "archive", req.Volumes[1].Pool)
}
