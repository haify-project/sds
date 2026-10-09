package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/haify-project/haify/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// encryptedCreateFake answers the cryptsetup pre-flight the way a node with a
// working crypt stack does, and otherwise behaves like the plain fake.
func encryptedCreateFake() *fakeDeploymentClient {
	return &fakeDeploymentClient{
		execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, "command -v cryptsetup") {
				return successExecResult(hosts, "ok"), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
}

// execIndexOf returns the position of the first exec call containing substr,
// or -1. Order matters on the teardown path: the key has to be destroyed
// before the extents holding the ciphertext are handed back.
func execIndexOf(dep *fakeDeploymentClient, substr string) int {
	for i, c := range dep.execCalls {
		if strings.Contains(c.cmd, substr) {
			return i
		}
	}
	return -1
}

func TestCreateEncryptedResourceBuildsContainersAndRecordsThem(t *testing.T) {
	dep := encryptedCreateFake()
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	registerNodes(ctrl, map[string]string{"node1": "10.0.0.1", "node2": "10.0.0.2"})

	ctx := context.Background()
	require.NoError(t, ctrl.resources.CreateResourceWithVolumesMetadata(ctx,
		"secret", 7100, []string{"node1", "node2"}, "C", "lvm", nil,
		[]VolumeSpec{{SizeGB: 4, Pool: "vg0"}}, nil, ResourceMetadata{Encrypt: true}))

	stored, err := ctrl.db.GetResource(ctx, "secret")
	require.NoError(t, err)
	assert.True(t, stored.Encrypted, "the encryption state must survive a controller restart")

	vols, err := ctrl.db.ListVolumes(ctx, "secret")
	require.NoError(t, err)
	require.Len(t, vols, 1)
	// The recorded device is what teardown and resize read back to decide what
	// they are looking at, exactly as they already do for a zvol.
	assert.Equal(t, "/dev/mapper/haify_haify_vg0_secret_data", vols[0].Device)

	cfg, ok := findDistributedConfig(dep, "/etc/drbd.d/secret.res")
	require.True(t, ok)
	assert.Contains(t, cfg, "disk      /dev/mapper/haify_haify_vg0_secret_data;")
	assert.NotContains(t, cfg, "/dev/haify_vg0/secret_data;",
		"DRBD pointed at the LV would write plaintext past the crypt layer")

	// Each node built its own container; nothing was copied between them.
	var formatted []string
	for _, c := range dep.execCalls {
		if strings.Contains(c.cmd, "luksFormat") {
			formatted = append(formatted, c.hosts...)
		}
	}
	assert.ElementsMatch(t, []string{"10.0.0.1", "10.0.0.2"}, formatted)

	// And nothing that could carry key material left the controller.
	for _, c := range dep.execCalls {
		assert.NotContains(t, c.cmd, "--key ", "a passphrase in argv is visible to ps on the node")
	}
	info, err := ctrl.resources.GetResource(ctx, "secret")
	require.NoError(t, err)
	assert.True(t, info.Encrypted, "an operator has to be able to see that a volume is encrypted")
	require.NotEmpty(t, info.Volumes)
	assert.True(t, info.Volumes[0].Encrypted)
}

// The pre-flight exists so a node that could never have succeeded is found
// before any storage is built — not after two of three replicas are encrypted.
func TestCreateEncryptedResourceRefusesANodeWithoutCryptsetup(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, "command -v cryptsetup") {
				if len(hosts) > 0 && hosts[0] == "10.0.0.2" {
					return successExecResult(hosts, "nocryptsetup"), nil
				}
				return successExecResult(hosts, "ok"), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	registerNodes(ctrl, map[string]string{"node1": "10.0.0.1", "node2": "10.0.0.2"})

	err := ctrl.resources.CreateResourceWithVolumesMetadata(context.Background(),
		"secret", 7100, []string{"node1", "node2"}, "C", "lvm", nil,
		[]VolumeSpec{{SizeGB: 4, Pool: "vg0"}}, nil, ResourceMetadata{Encrypt: true})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "node2")
	assert.Contains(t, err.Error(), "install cryptsetup")
	assert.Empty(t, dep.lvCreateCalls, "nothing may be provisioned for a request that cannot succeed")
}

// An encrypted resource that cannot reopen its containers at boot is a
// resource that comes back Diskless — discovered at the next reboot, which is
// the worst moment to find out. That failure is fatal here even though it is
// merely cosmetic for a plaintext resource.
func TestCreateEncryptedResourceFailsIfTheBootUnitCannotBeInstalled(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			switch {
			case strings.Contains(cmd, "command -v cryptsetup"):
				return successExecResult(hosts, "ok"), nil
			case strings.Contains(cmd, drbdBootUnitPath):
				return failedResult(hosts, "tee: /etc/systemd/system: Read-only file system"), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	registerNodes(ctrl, map[string]string{"node1": "10.0.0.1", "node2": "10.0.0.2"})

	err := ctrl.resources.CreateResourceWithVolumesMetadata(context.Background(),
		"secret", 7100, []string{"node1", "node2"}, "C", "lvm", nil,
		[]VolumeSpec{{SizeGB: 4, Pool: "vg0"}}, nil, ResourceMetadata{Encrypt: true})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "opens the LUKS containers")

	// The rollback has to take the containers down too: an open container holds
	// the LV, and a key left behind outlives what it was protecting.
	assert.NotEqual(t, -1, execIndexOf(dep, "cryptsetup close haify_haify_vg0_secret_data"))
}

// The same failure is survivable — and stays non-fatal — without encryption.
func TestCreatePlaintextResourceToleratesAMissingBootUnit(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, drbdBootUnitPath) {
				return failedResult(hosts, "tee: /etc/systemd/system: Read-only file system"), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	registerNodes(ctrl, map[string]string{"node1": "10.0.0.1", "node2": "10.0.0.2"})

	require.NoError(t, ctrl.resources.CreateResourceWithVolumes(context.Background(),
		"plain", 7100, []string{"node1", "node2"}, "C", "lvm", nil,
		[]VolumeSpec{{SizeGB: 4, Pool: "vg0"}}, nil))
}

func TestCreateEncryptedResourceRefusesZFSBackedPools(t *testing.T) {
	dep := encryptedCreateFake()
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	registerNodes(ctrl, map[string]string{"node1": "10.0.0.1", "node2": "10.0.0.2"})

	err := ctrl.resources.CreateResourceWithVolumesMetadata(context.Background(),
		"secret", 7100, []string{"node1", "node2"}, "C", "zfs", nil,
		[]VolumeSpec{{SizeGB: 4, Pool: "tank"}}, nil, ResourceMetadata{Encrypt: true})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "LVM-backed pools")
	assert.Empty(t, dep.zfsCreatePoolCalls)
}

// Deleting is where "encrypted at rest" is either honoured or not: the freed
// extents still hold the ciphertext, so the key has to be gone before them.
func TestDeleteEncryptedResourceDestroysTheKeysFirst(t *testing.T) {
	dep := encryptedCreateFake()
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	registerNodes(ctrl, map[string]string{"node1": "10.0.0.1", "node2": "10.0.0.2"})

	ctx := context.Background()
	require.NoError(t, ctrl.resources.CreateResourceWithVolumesMetadata(ctx,
		"secret", 7100, []string{"node1", "node2"}, "C", "lvm", nil,
		[]VolumeSpec{{SizeGB: 4, Pool: "vg0"}}, nil, ResourceMetadata{Encrypt: true}))

	dep.execCalls = nil
	require.NoError(t, ctrl.resources.DeleteResource(ctx, "secret", false))

	shred := execIndexOf(dep, "shred -u /etc/haify/luks/haify_haify_vg0_secret_data.key")
	remove := execIndexOf(dep, "lvremove -f haify_vg0/secret_data")
	require.NotEqual(t, -1, shred, "the key must be destroyed, not merely unlinked")
	require.NotEqual(t, -1, remove)
	assert.Less(t, shred, remove,
		"the key has to go before the extents that still hold the ciphertext")
	assert.NotEqual(t, -1, execIndexOf(dep, "cryptsetup close haify_haify_vg0_secret_data"),
		"an open container holds the LV and lvremove would refuse")
}

// A volume added to an encrypted resource is encrypted too — otherwise one
// volume of the resource silently holds plaintext.
func TestAddVolumeInheritsEncryptionFromTheResource(t *testing.T) {
	dep := encryptedCreateFake()
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	registerNodes(ctrl, map[string]string{"node1": "10.0.0.1", "node2": "10.0.0.2"})

	ctx := context.Background()
	require.NoError(t, ctrl.resources.CreateResourceWithVolumesMetadata(ctx,
		"secret", 7100, []string{"node1", "node2"}, "C", "lvm", nil,
		[]VolumeSpec{{SizeGB: 4, Pool: "vg0"}}, nil, ResourceMetadata{Encrypt: true}))

	cfg, ok := findDistributedConfig(dep, "/etc/drbd.d/secret.res")
	require.True(t, ok)
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if strings.Contains(cmd, "cat /etc/drbd.d/secret.res") {
			return successExecResult(hosts, cfg), nil
		}
		if strings.Contains(cmd, "command -v cryptsetup") {
			return successExecResult(hosts, "ok"), nil
		}
		if out, ok := settledVolumeStatus(cmd); ok {
			return successExecResult(hosts, out), nil
		}
		return successExecResult(hosts, ""), nil
	}
	dep.execCalls = nil
	dep.distributedConfigs = nil

	require.NoError(t, ctrl.resources.AddVolume(ctx, "secret", "secret_state1", "vg0", 1))

	assert.NotEqual(t, -1, execIndexOf(dep, "luksFormat"),
		"the added volume must be wrapped like the rest of the resource")

	updated, ok := findDistributedConfig(dep, "/etc/drbd.d/secret.res")
	require.True(t, ok)
	assert.Contains(t, updated, "disk      /dev/mapper/haify_haify_vg0_secret_state1;")

	vols, err := ctrl.db.ListVolumes(ctx, "secret")
	require.NoError(t, err)
	for _, v := range vols {
		if v.VolumeName == "secret_state1" {
			assert.Equal(t, "/dev/mapper/haify_haify_vg0_secret_state1", v.Device)
		}
	}
}

// Growing an encrypted volume means growing two devices. Skipping the mapping
// makes `drbdadm resize` find nothing new and report success on a resize that
// did not happen.
func TestResizeEncryptedVolumeGrowsTheMappingToo(t *testing.T) {
	dep := encryptedCreateFake()
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	registerNodes(ctrl, map[string]string{"node1": "10.0.0.1", "node2": "10.0.0.2"})

	ctx := context.Background()
	require.NoError(t, ctrl.resources.CreateResourceWithVolumesMetadata(ctx,
		"secret", 7100, []string{"node1", "node2"}, "C", "lvm", nil,
		[]VolumeSpec{{SizeGB: 4, Pool: "vg0"}}, nil, ResourceMetadata{Encrypt: true}))

	cfg, ok := findDistributedConfig(dep, "/etc/drbd.d/secret.res")
	require.True(t, ok)
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if strings.Contains(cmd, "cat /etc/drbd.d/secret.res") {
			return successExecResult(hosts, cfg), nil
		}
		return successExecResult(hosts, ""), nil
	}
	dep.execCalls = nil

	require.NoError(t, ctrl.resources.ResizeVolume(ctx, "secret", 0, 8))

	lvresize := execIndexOf(dep, "lvresize")
	cryptResize := execIndexOf(dep, "cryptsetup resize")
	drbdResize := execIndexOf(dep, "drbdadm resize")
	require.NotEqual(t, -1, lvresize)
	require.NotEqual(t, -1, cryptResize)
	require.NotEqual(t, -1, drbdResize)
	assert.Less(t, lvresize, cryptResize, "the mapping can only grow into extents the LV already has")
	assert.Less(t, cryptResize, drbdResize, "DRBD measures the mapping, not the LV")

	// lvresize must address the LV; /dev/mapper/... is not an LV at all.
	assert.Contains(t, dep.execCalls[lvresize].cmd, "/dev/haify_vg0/secret_data")
	assert.NotContains(t, dep.execCalls[lvresize].cmd, "/dev/mapper/")
}
