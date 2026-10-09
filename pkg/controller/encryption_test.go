package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The whole point of the key handling is what is ABSENT from these commands:
// no passphrase in argv (dispatch puts the command in the remote sshd's argv,
// where `ps` can read it, and deployment.Exec logs it verbatim at Debug), and
// no key material generated anywhere but on the node itself.

func TestLUKSProvisionNeverPutsAKeyOnTheCommandLine(t *testing.T) {
	cmd, err := luksProvisionCmd("vg0", "res1_data", "/dev/vg0/res1_data")
	require.NoError(t, err)

	// cryptsetup's passphrase-bearing options. If any of these ever appear the
	// secret is in argv on the node and in the controller's debug log.
	for _, forbidden := range []string{"--key ", "echo ", "printf %s $", "--test-passphrase", "<<<"} {
		assert.NotContains(t, cmd, forbidden,
			"the passphrase must never travel through the command string")
	}
	assert.Contains(t, cmd, "--key-file /etc/haify/luks/haify_vg0_res1_data.key")
	assert.Contains(t, cmd, "dd if=/dev/urandom of=/etc/haify/luks/haify_vg0_res1_data.key",
		"the key is generated on the node, not sent to it")

	// A key readable by anyone but root defeats the exercise on a multi-user
	// node, and the directory mode is what protects it before the chmod lands.
	assert.Contains(t, cmd, "install -d -m 0700 -o root -g root /etc/haify/luks")
	assert.Contains(t, cmd, "chmod 0400 /etc/haify/luks/haify_vg0_res1_data.key")
}

func TestLUKSProvisionIsRerunnable(t *testing.T) {
	cmd, err := luksProvisionCmd("vg0", "res1_data", "/dev/vg0/res1_data")
	require.NoError(t, err)

	// Key AND header present ⇒ leave both alone. Either missing ⇒ rebuild the
	// pair. A luksFormat that ran unconditionally would wipe the container on
	// every retry of a partially failed create.
	assert.Contains(t, cmd, "if sudo test -f /etc/haify/luks/haify_vg0_res1_data.key && sudo cryptsetup isLuks /dev/vg0/res1_data")
	assert.Contains(t, cmd, "luksFormat --batch-mode --type luks2")
	// Opening an already-open container fails; the boot path reruns this shape.
	assert.Contains(t, cmd, "if [ ! -e /dev/mapper/haify_vg0_res1_data ]")
}

// Key stretching buys nothing against 512 bits of /dev/urandom, and argon2id's
// default cost scales with node RAM — paid again on every container at boot.
func TestLUKSProvisionUsesACheapKDFForAMachineGeneratedKey(t *testing.T) {
	cmd, err := luksProvisionCmd("vg0", "res1_data", "/dev/vg0/res1_data")
	require.NoError(t, err)
	assert.Contains(t, cmd, "--pbkdf pbkdf2 --pbkdf-force-iterations 1000")
}

func TestLUKSTeardownDestroysTheKeyBeforeTheStorageGoes(t *testing.T) {
	cmd, err := luksTeardownCmd("vg0", "res1_data")
	require.NoError(t, err)

	assert.Contains(t, cmd, "cryptsetup close haify_vg0_res1_data")
	// Unlinking alone leaves the 64 bytes in whatever extent the root
	// filesystem hands out next — which is the only thing making a
	// decommissioned pool disk unreadable.
	assert.Contains(t, cmd, "shred -u /etc/haify/luks/haify_vg0_res1_data.key")
	assert.Contains(t, cmd, "dd if=/dev/urandom of=/etc/haify/luks/haify_vg0_res1_data.key",
		"a node without shred still has to overwrite it")
	assert.Contains(t, cmd, "rm -f /etc/haify/luks/haify_vg0_res1_data.key /etc/haify/luks/haify_vg0_res1_data.dev")
}

// Pool and volume names reach a shell command and a device path. Everything on
// the create path has already been through normalizeManagedName, so this is the
// backstop for a caller that has not.
func TestLUKSNamesAreValidatedBeforeReachingAShell(t *testing.T) {
	tests := []struct {
		name       string
		pool, vol  string
		wantReject bool
	}{
		{name: "ordinary", pool: "vg0", vol: "res1_data"},
		{name: "dashes and dots", pool: "haify-pool.1", vol: "my-res_data"},
		{name: "command substitution in pool", pool: "vg0$(id)", vol: "res1_data", wantReject: true},
		{name: "semicolon in volume", pool: "vg0", vol: "res1;rm -rf /", wantReject: true},
		{name: "path traversal", pool: "../../etc", vol: "res1_data", wantReject: true},
		{name: "backtick", pool: "vg0", vol: "a`whoami`", wantReject: true},
		{name: "empty pool", pool: "", vol: "res1_data", wantReject: true},
		{name: "leading dash looks like a flag", pool: "-x", vol: "res1_data", wantReject: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateLUKSNames(tc.pool, tc.vol)
			if tc.wantReject {
				require.Error(t, err)
				_, perr := luksProvisionCmd(tc.pool, tc.vol, "/dev/x/y")
				assert.Error(t, perr, "the command builders must refuse it too")
				_, terr := luksTeardownCmd(tc.pool, tc.vol)
				assert.Error(t, terr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// The device path is interpolated into the same command, so it gets the same
// treatment as the names — even though every caller today derives it from names
// that have already been checked.
func TestLUKSProvisionRejectsAnUnsafeDevicePath(t *testing.T) {
	for _, device := range []string{
		"/dev/vg0/res1_data; rm -rf /",
		"$(cat /etc/shadow)",
		"/tmp/not-a-device",
		"",
	} {
		_, err := luksProvisionCmd("vg0", "res1_data", device)
		assert.Error(t, err, "device %q must be refused", device)
	}
	_, err := luksProvisionCmd("vg0", "res1_data", "/dev/vg0/res1_data")
	assert.NoError(t, err)
}

// The container must be open before DRBD attaches, on every replica — so the
// open belongs in the boot script between LVM activation and `drbdadm adjust`,
// not in a promoter hook that runs only on the node being promoted and only
// once DRBD is already up.
func TestBootScriptOpensContainersBetweenLVMAndDRBD(t *testing.T) {
	script := drbdBootUnitInstallCmd()

	lvm := strings.Index(script, "vgchange -ay")
	open := strings.Index(script, `cryptsetup open --type luks --key-file "$ckey"`)
	adjust := strings.Index(script, `"$DRBDADM" adjust "$res"`)

	require.Positive(t, lvm)
	require.Positive(t, open)
	require.Positive(t, adjust)
	assert.Less(t, lvm, open, "the LV must exist before its container can be opened")
	assert.Less(t, open, adjust, "DRBD must not attach before the container is open")

	// Driven by what is on the node, so it keeps working for resources created
	// after the unit was written.
	assert.Contains(t, script, "for ptr in /etc/haify/luks/*.dev")
	// One unopenable container must not keep every other resource down.
	assert.Contains(t, script, `"$cname" >/dev/null 2>&1 || true`)
	// The unit is what makes an encrypted resource survive a reboot, so it has
	// to run before drbd-reactor tries to promote anything.
	assert.Contains(t, script, "Before=drbd-reactor.service")
}

func TestBackingVolumeSizeLeavesRoomForTheLUKSHeader(t *testing.T) {
	tests := []struct {
		name      string
		sizeGB    uint32
		peers     int
		encrypted bool
	}{
		{name: "plain 4 GiB", sizeGB: 4, peers: 2},
		{name: "encrypted 4 GiB", sizeGB: 4, peers: 2, encrypted: true},
		{name: "encrypted 1 TiB", sizeGB: 1024, peers: 7, encrypted: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := uint64(tc.sizeGB) * 1024 * 1024 * 1024
			got := backingVolumeSizeBytes(tc.sizeGB, tc.peers, tc.encrypted)
			plain := backingVolumeSizeBytes(tc.sizeGB, tc.peers, false)

			assert.Greater(t, got, data, "the device DRBD exports must not come out short")
			if tc.encrypted {
				// The header sits at the front and is not part of the mapping,
				// so it is pure overhead on top of the DRBD allowance.
				assert.Equal(t, plain+luksHeaderBytes, got)
			} else {
				assert.Equal(t, plain, got)
			}
		})
	}
}

func TestAssertEncryptionSupportedReadsTheProbe(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		wantErr string
	}{
		{name: "supported", output: "ok"},
		{name: "no cryptsetup", output: "nocryptsetup", wantErr: "no cryptsetup"},
		{name: "no dm-crypt", output: "nodmcrypt", wantErr: "no dm-crypt target"},
		// Silence is not consent: a probe that answered nothing tells us
		// nothing, and guessing "supported" is how a plaintext replica gets
		// built inside a resource that claims to be encrypted.
		{name: "unreadable", output: "", wantErr: "could not tell"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dep := &fakeDeploymentClient{
				execFunc: func(_ context.Context, hosts []string, _ string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
					return successExecResult(hosts, tc.output), nil
				},
			}
			ctrl := newBasicTestController(dep)
			err := ctrl.resources.assertEncryptionSupported(context.Background(),
				[]string{"10.0.0.1"}, []string{"node1"})
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Contains(t, err.Error(), "node1", "the message must name the node")
		})
	}
}

func TestAssertEncryptableStorageRefusesZFS(t *testing.T) {
	tests := []struct {
		storageType string
		wantErr     bool
	}{
		{storageType: ""},
		{storageType: "lvm"},
		{storageType: "lvm-thin"},
		{storageType: "zfs", wantErr: true},
		{storageType: "zfs-thin", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.storageType, func(t *testing.T) {
			err := assertEncryptableStorage(tc.storageType)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "LVM-backed pools")
				return
			}
			assert.NoError(t, err)
		})
	}
}

// Half-converting a resource in place would leave some replicas encrypted and
// some not, with nothing in the config to say which — a security claim that
// quietly is not true. Refusing is the whole feature here.
func TestEncryptionCannotBeRetrofittedOntoAnExistingResource(t *testing.T) {
	tests := []struct {
		name     string
		existing *database.Resource
		want     bool
		wantErr  string
	}{
		{name: "brand new name", want: true},
		{
			name:     "turning it on afterwards",
			existing: &database.Resource{Name: "data", Encrypted: false},
			want:     true,
			wantErr:  "cannot be added in place",
		},
		{
			name:     "turning it off afterwards",
			existing: &database.Resource{Name: "data", Encrypted: true},
			want:     false,
			wantErr:  "already exists encrypted",
		},
		{
			name:     "unchanged, encrypted",
			existing: &database.Resource{Name: "data", Encrypted: true},
			want:     true,
		},
		{
			name:     "unchanged, plaintext",
			existing: &database.Resource{Name: "data", Encrypted: false},
			want:     false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := newBasicTestController(&fakeDeploymentClient{})
			ctrl.db = newTestDB(t)
			if tc.existing != nil {
				require.NoError(t, ctrl.db.SaveResource(context.Background(), tc.existing))
			}
			err := ctrl.resources.assertEncryptionNotRetrofitted(context.Background(), "data", tc.want)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// LVM addresses the logical volume; DRBD addresses the mapping on top of it.
// Aiming lvresize/lvremove at /dev/mapper/... does not address an LV at all.
func TestBackingLVForResolvesThroughTheVolumeRecord(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "data", Encrypted: true}))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{
		ResourceName: "data", VolumeName: "data_data", VolumeID: 0, Pool: "vg0",
		Device: "/dev/mapper/haify_vg0_data_data",
	}))

	lv, pool, vol, err := ctrl.resources.backingLVFor(ctx, "data", 0, "/dev/mapper/haify_vg0_data_data")
	require.NoError(t, err)
	assert.Equal(t, "/dev/vg0/data_data", lv)
	assert.Equal(t, "vg0", pool)
	assert.Equal(t, "data_data", vol)

	// An unencrypted disk path is already the LV and needs no lookup.
	lv, pool, _, err = ctrl.resources.backingLVFor(ctx, "data", 0, "/dev/vg0/data_data")
	require.NoError(t, err)
	assert.Equal(t, "/dev/vg0/data_data", lv)
	assert.Empty(t, pool)

	// The container name cannot be split back into pool and volume — both may
	// contain underscores — so a missing record is an error, never a guess.
	_, _, _, err = ctrl.resources.backingLVFor(ctx, "data", 7, "/dev/mapper/haify_vg0_data_vol7")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be identified")

	// An ADOPTED foreign resource may live under /dev/mapper for reasons of its
	// own — multipath, someone else's dm stack, someone else's LUKS. Haify has no
	// key for it and must not start re-plumbing it, so the recorded flag and
	// not the path prefix is what decides.
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "foreign"}))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{
		ResourceName: "foreign", VolumeName: "mpatha", VolumeID: 0, Pool: "mapper",
		Device: "/dev/mapper/mpatha",
	}))
	lv, pool, _, err = ctrl.resources.backingLVFor(ctx, "foreign", 0, "/dev/mapper/mpatha")
	require.NoError(t, err)
	assert.Equal(t, "/dev/mapper/mpatha", lv, "a foreign mapping is left exactly as it was")
	assert.Empty(t, pool, "no crypt container is claimed for it")
}

// DRBD must attach to the mapping. Pointed at the LV it would write plaintext
// straight past the layer that exists to encrypt it.
func TestGeneratedConfigPointsDRBDAtTheContainer(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	registerNodes(ctrl, map[string]string{"node1": "10.0.0.1", "node2": "10.0.0.2"})

	cfg := ctrl.resources.generateDrbdConfig(
		"res1", 7001,
		[]resolvedVolume{{id: 0, minor: 0, pool: "vg0", volumeName: "res1_data", encrypted: true}},
		[]string{"node1", "node2"}, nil, "C", "lvm", nil, nil)

	assert.Contains(t, cfg, "disk      /dev/mapper/haify_vg0_res1_data;")
	assert.NotContains(t, cfg, "disk      /dev/vg0/res1_data;")
}

func TestGeneratedConfigStillPointsAtTheLVWhenNotEncrypted(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	registerNodes(ctrl, map[string]string{"node1": "10.0.0.1", "node2": "10.0.0.2"})

	cfg := ctrl.resources.generateDrbdConfig(
		"res1", 7001,
		[]resolvedVolume{{id: 0, minor: 0, pool: "vg0", volumeName: "res1_data"}},
		[]string{"node1", "node2"}, nil, "C", "lvm", nil, nil)

	assert.Contains(t, cfg, "disk      /dev/vg0/res1_data;")
	assert.NotContains(t, cfg, "/dev/mapper/")
}

// A node added to an encrypted resource with a plaintext backing volume would
// hold a full readable copy of data every other node keeps encrypted, and
// nothing in the config or the status output would say so.
func TestAddReplicaConfigUsesTheContainerOnAnEncryptedResource(t *testing.T) {
	rm := addDRTestFixture(t)
	rm.controller.nodes.nodes["192.168.1.30"] = &NodeInfo{
		Name: "node-d", Address: "192.168.1.30", Hostname: "haify-d", State: NodeStateOnline,
	}
	rm.controller.hostsMap["node-d"] = "192.168.1.30"

	out, err := rm.addReplicaToConfig(lanResConfig, "openclaw", "node-d", "192.168.1.30",
		addDRVolumes, 7300, 2, 0, false, "", true)
	require.NoError(t, err)

	stanza := out[strings.Index(out, "on haify-d {"):]
	for _, v := range addDRVolumes {
		assert.Contains(t, stanza, "disk      "+luksMapperPath(v.Pool, v.VolumeName)+";")
	}
}

func TestCreateBackingVolumeWrapsEachNodesCopy(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)

	require.NoError(t, ctrl.resources.createBackingVolume(context.Background(),
		[]string{"10.0.0.1", "10.0.0.2"}, []string{"node1", "node2"},
		"lvm", "vg0", "res1_data", 4, true))

	// One provisioning run per node: the key is per node, generated there.
	var provisioned []string
	for _, c := range dep.execCalls {
		if strings.Contains(c.cmd, "luksFormat") {
			provisioned = append(provisioned, c.hosts...)
		}
	}
	assert.ElementsMatch(t, []string{"10.0.0.1", "10.0.0.2"}, provisioned,
		"every replica encrypts its own copy")

	// The LV is asked for with room for the header on top of DRBD's metadata.
	require.Len(t, dep.lvCreateCalls, 2)
	assert.Equal(t, fmt.Sprintf("%dB", backingVolumeSizeBytes(4, 1, true)),
		dep.lvCreateCalls[0].size)
}
