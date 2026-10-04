package controller

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/sds/pkg/database"
)

// encryptFixture marks the fixture's resource as SDS-encrypted, the way
// resource create records it: the flag on the resource and the crypt mapping
// as the volume's device.
func encryptFixture(t *testing.T, ctrl *Controller) {
	t.Helper()
	ctx := context.Background()
	res, err := ctrl.db.GetResource(ctx, "data")
	require.NoError(t, err)
	res.Encrypted = true
	require.NoError(t, ctrl.db.SaveResource(ctx, res))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{
		ResourceName: "data", VolumeName: "data_data", VolumeID: 0, Pool: "vg0", SizeGB: 4,
		Device: luksMapperPath("vg0", "data_data"),
	}))
}

// Reading an encrypted volume's snapshot directly stored its LUKS header and
// ciphertext, which restored as noise. It has to be read through a crypt
// mapping opened with that volume's own key.
func TestBackupOfEncryptedVolumeReadsThroughLUKS(t *testing.T) {
	stub := &backupExecStub{storedBytes: backupVolumeBytes}
	ctrl := newBackupFixture(t, stub, "Secondary")
	encryptFixture(t, ctrl)

	rec, err := ctrl.backups.CreateBackup(context.Background(), "data", "offsite", "", false)
	require.NoError(t, err)

	require.Len(t, stub.uploads, 1)
	up := stub.uploads[0]
	assert.Contains(t, up, "cryptsetup open --type luks --readonly --key-file "+luksKeyPath(luksContainerName("vg0", "data_data")))
	assert.Contains(t, up, "/dev/vg0/data_data"+backupSnapMarker, "the snapshot is opened, never the live volume")
	assert.Contains(t, up, `dd if="$SRC"`)
	assert.Contains(t, up, `cryptsetup close "$MAP"`, "the mapping is closed so the snapshot can be removed")

	require.Len(t, rec.Volumes, 1)
	assert.True(t, rec.Volumes[0].ReadThroughLUKS)

	stored, err := ctrl.db.GetBackup(context.Background(), rec.ID)
	require.NoError(t, err)
	assert.True(t, stored.Volumes[0].ReadThroughLUKS, "the marker is persisted")
}

// A backup taken before the fix holds ciphertext and says nothing about it.
// Restoring it would overwrite a volume with noise and report success.
func TestRestoreRefusesCiphertextBackupOfEncryptedResource(t *testing.T) {
	stub := &backupExecStub{storedBytes: backupVolumeBytes}
	ctrl := newBackupFixture(t, stub, "Secondary")
	encryptFixture(t, ctrl)
	ctx := context.Background()

	old := &database.Backup{
		ID: "data_20260901T000000Z", Resource: "data", Target: "offsite", Node: "10.0.0.2",
		State: database.BackupStateCompleted, Kind: database.BackupKindFull, Prefix: "data/data_20260901T000000Z",
		Volumes: []database.BackupVolume{{VolumeID: 0, Pool: "vg0", BackingVolume: "data_data",
			Object: "data/data_20260901T000000Z/volume-0.img.gz", Bytes: backupVolumeBytes}},
	}
	require.NoError(t, ctrl.db.SaveBackup(ctx, old))

	_, err := ctrl.backups.RestoreBackup(ctx, old.ID, "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ciphertext")

	// The same record, read through LUKS, passes this check.
	old.Volumes[0].ReadThroughLUKS = true
	require.NoError(t, ctrl.backups.assertNotCiphertext(ctx, []*database.Backup{old}))
}

// An unencrypted resource's backups never carried the marker and need none.
func TestCiphertextCheckIgnoresUnencryptedResources(t *testing.T) {
	stub := &backupExecStub{storedBytes: backupVolumeBytes}
	ctrl := newBackupFixture(t, stub, "Secondary")
	rec := &database.Backup{ID: "x", Resource: "data",
		Volumes: []database.BackupVolume{{VolumeID: 0}}}
	assert.NoError(t, ctrl.backups.assertNotCiphertext(context.Background(), []*database.Backup{rec}))
}

// Changes computed on top of a ciphertext image restore as noise too, so the
// first backup after the fix must be a full one.
func TestIncrementalNeverBuildsOnCiphertextBase(t *testing.T) {
	f := newIncrementalFixture(t)
	encryptFixture(t, f.ctrl)
	first := f.backup(t, false)
	require.True(t, first.Volumes[0].ReadThroughLUKS)

	// Turn the first backup into one an earlier version would have recorded.
	first.Volumes[0].ReadThroughLUKS = false
	require.NoError(t, f.ctrl.db.SaveBackup(context.Background(), first))

	second := f.backup(t, false)
	assert.Equal(t, database.BackupKindFull, second.Kind)

	third := f.backup(t, false)
	assert.Equal(t, database.BackupKindIncremental, third.Kind)
	assert.True(t, third.Volumes[0].ReadThroughLUKS)
	deltas := f.ran("thin_delta")
	require.NotEmpty(t, deltas)
	last := deltas[len(deltas)-1]
	assert.Contains(t, last, "cryptsetup open --type luks --readonly")
	assert.Contains(t, last, `-v off="$OFF"`)
	assert.Contains(t, last, `dd if="$SRC"`)
}

// thin_delta addresses the LV; the plaintext starts after the LUKS header. The
// ranges are shifted by it, and a change inside the header itself is dropped.
func TestThinDeltaRangesAreShiftedPastTheLUKSHeader(t *testing.T) {
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("no awk")
	}
	// 64 KiB blocks. Block 100 is inside a 16 MiB header (256 blocks); the
	// range 250-260 straddles its end; block 800 is well past it.
	xml := `<superblock uuid="" time="2" transaction="3" data_block_size="128" nr_data_blocks="0">
  <diff left="2" right="3">
    <different begin="100" length="3"/>
    <different begin="250" length="10"/>
    <right_only begin="800" length="32"/>
  </diff>
</superblock>`
	cmd := exec.Command("awk", "-v", "size=104857600", "-v", fmt.Sprintf("off=%d", 16<<20),
		"-v", "gap=0", thinDeltaAwk)
	cmd.Stdin = strings.NewReader(xml)
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("0 %d\n%d %d\n", 4*65536, (800-256)*65536, 32*65536), string(out))
}

// The node-side script is run for real against stand-ins for sudo, cryptsetup
// and dmsetup, so a quoting slip or a wrong table field fails here and not on
// a node in the middle of the night.
func TestBackupSnapshotSourceScriptFindsTheLUKSOffset(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	stubs := map[string]string{
		// test is a shell builtin, so the key-file check is answered here.
		"sudo":       `[ "$1" = test ] && exit 0; "$@"`,
		"cryptsetup": `echo "cryptsetup $*" >> "` + log + `"`,
		"udevadm":    `exit 0`,
		// What dmsetup prints for a LUKS2 mapping: the key is a keyring
		// reference, the offset (in sectors) is the last field.
		"dmsetup": `echo "0 8388608 crypt aes-xts-plain64 :64:logon:cryptsetup:abc-d0 0 253:7 32768"`,
	}
	for name, body := range stubs {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/bash\n"+body+"\n"), 0o755))
	}

	setup, cleanup, err := backupSnapshotSource("vg0", "data_data", "data_data_bk_20261004T000000Z", true)
	require.NoError(t, err)
	script := "set -e -o pipefail; MAP=\ntrap '" + cleanup + "' EXIT\n" + setup + "\necho \"SRC=$SRC OFF=$OFF\""
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Regexp(t, `SRC=/dev/mapper/sdsbk_[0-9a-f]{16} OFF=16777216`, string(out))

	logged, err := os.ReadFile(log)
	require.NoError(t, err)
	assert.Contains(t, string(logged), "open --type luks --readonly --key-file /etc/sds/luks/sds_vg0_data_data.key /dev/vg0/data_data_bk_20261004T000000Z sdsbk_")
	assert.Contains(t, string(logged), "close sdsbk_", "the mapping is closed on exit")

	_, _, err = backupSnapshotSource("vg0", "data;rm", "s", true)
	assert.Error(t, err, "names are validated before they reach a shell")
}
