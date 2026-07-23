package main

import (
	"testing"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResourceCreate_ProfileAndLabelFlags(t *testing.T) {
	cmd := resourceCreate()

	require.NoError(t, cmd.ParseFlags([]string{
		"--profile", "production",
		"--label", "tier=critical",
		"--label", "app=postgres",
	}))

	profile, err := cmd.Flags().GetString("profile")
	require.NoError(t, err)
	labels, err := cmd.Flags().GetStringToString("label")
	require.NoError(t, err)
	protocol, err := cmd.Flags().GetString("protocol")
	require.NoError(t, err)
	storageType, err := cmd.Flags().GetString("storage-type")
	require.NoError(t, err)

	assert.Equal(t, "production", profile)
	assert.Equal(t, map[string]string{"app": "postgres", "tier": "critical"}, labels)
	assert.Equal(t, "C", protocol)
	assert.Equal(t, "lvm", storageType)
}

func TestResourceCommand_ProfileCRUDCommands(t *testing.T) {
	profile, _, err := resourceCommand().Find([]string{"profile"})
	require.NoError(t, err)
	require.NotNil(t, profile)

	for _, name := range []string{"create", "get", "list", "delete"} {
		child, _, findErr := profile.Find([]string{name})
		require.NoError(t, findErr)
		assert.Equal(t, name, child.Name())
	}
}

func TestFormatResourceProfile_SortsMapAndSliceFields(t *testing.T) {
	profile := &sdspb.ResourceProfile{
		Name:                "production",
		Protocol:            "C",
		StorageType:         "lvm-thin",
		Pool:                "fast",
		Replicas:            3,
		ReplicasOnDifferent: []string{"zone", "rack"},
		ReplicasOnSame:      []string{"region"},
		DrbdOptions:         map[string]string{"net/max-buffers": "8000", "disk/on-io-error": "detach"},
		Labels:              map[string]string{"tier": "critical", "app": "postgres"},
	}

	assert.Equal(t,
		"production (protocol=C, storage-type=lvm-thin, pool=fast, replicas=3, replicas-on-different=rack, zone, replicas-on-same=region, drbd-options=disk/on-io-error=detach, net/max-buffers=8000, labels=app=postgres, tier=critical)",
		formatResourceProfile(profile),
	)
}

func TestProfileCreateValue(t *testing.T) {
	assert.Equal(t, "from profile production", profileCreateValue("", "production"))
	assert.Equal(t, "fast", profileCreateValue("fast", "production"))
	assert.Equal(t, "(none)", profileCreateValue("", ""))
}
