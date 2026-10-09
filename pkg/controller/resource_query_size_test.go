package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/sds/pkg/database"
)

// Listing reports a volume's exact size just as getting it does: Proxmox
// rescans by listing, and recorded a 5371715584-byte disk as 6 GiB when the
// list left the exact size out.
func TestListResourcesReportsExactSize(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "pve-103-0", Port: 7007, Nodes: "n1,n2", Protocol: "C", Replicas: 2}))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: "pve-103-0", VolumeName: "pve-103-0_data",
		VolumeID: 0, Pool: "sds_vg0", SizeGB: 6, SizeBytes: 5371715584}))

	list, err := ctrl.resources.ListResources(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Len(t, list[0].Volumes, 1)
	assert.Equal(t, uint64(5371715584), list[0].Volumes[0].SizeBytes)
	assert.Equal(t, uint64(6), list[0].Volumes[0].SizeGB)
}
