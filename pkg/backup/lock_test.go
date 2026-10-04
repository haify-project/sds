package backup

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lockedTarget() TargetSpec {
	t := s3Target()
	t.LockMode, t.LockDays = LockCompliance, 30
	return t
}

func TestLockSettingsAreValidated(t *testing.T) {
	require.NoError(t, lockedTarget().Validate())

	smb := TargetSpec{Name: "nas", Kind: KindSMB, Host: "nas", Share: "b", LockMode: LockGovernance, LockDays: 7}
	assert.ErrorContains(t, smb.Validate(), "S3 feature", "only S3 can lock")

	noDays := lockedTarget()
	noDays.LockDays = 0
	assert.ErrorContains(t, noDays.Validate(), "lock days")

	stray := s3Target()
	stray.LockDays = 7
	assert.ErrorContains(t, stray.Validate(), "without a lock mode")

	_, err := ParseLockMode("legal-hold")
	assert.Error(t, err)
	m, err := ParseLockMode(" Governance ")
	require.NoError(t, err)
	assert.Equal(t, LockGovernance, m)

	assert.Equal(t, DefaultFullEveryDays, lockedTarget().FullEvery())
	assert.Contains(t, lockedTarget().Describe(), "object lock compliance 30d, full every 7d")
}

// An rclone too old to lock uploads them unlocked and exits 0. That backup
// would look immutable and be deletable by anyone with the keys.
func TestPreflightRequiresAnRcloneThatLocks(t *testing.T) {
	version := "rclone v1.73.2"
	dep := &fakeDeploy{execFunc: func(string) (*Result, error) { return okResult([]string{"n"}, version+"\n"), nil }}

	err := NewRclone().Preflight(context.Background(), dep, "n", lockedTarget())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs rclone 1.74.0 or later")

	require.NoError(t, NewRclone().Preflight(context.Background(), dep, "n", s3Target()), "an unlocked target does not care")

	version = "rclone v1.74.0"
	require.NoError(t, NewRclone().Preflight(context.Background(), dep, "n", lockedTarget()))
	version = "rclone v1.75.1-beta.8812"
	require.NoError(t, NewRclone().Preflight(context.Background(), dep, "n", lockedTarget()))
	version = "rclone vDEV"
	assert.Error(t, NewRclone().Preflight(context.Background(), dep, "n", lockedTarget()), "unknown counts as too old")
}

func TestLockedPushesAndPinnedReads(t *testing.T) {
	sess, err := NewRclone().Prepare(context.Background(), &fakeDeploy{}, "n", lockedTarget())
	require.NoError(t, err)
	assert.NotContains(t, sess.PushCmd("a.img", 1<<20), "object-lock", "nothing is locked until asked")

	until := time.Date(2026, 11, 3, 4, 5, 6, 0, time.UTC)
	sess.SetLock(LockCompliance, until)
	push := sess.PushCmd("a.img", 1<<20)
	assert.Contains(t, push, "--s3-object-lock-mode COMPLIANCE --s3-object-lock-retain-until-date 2026-11-03T04:05:06Z")
	assert.True(t, strings.Index(push, "object-lock") < strings.Index(push, " rcat "), "flags go before the subcommand's arguments")

	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	sess.SetReadAt(at)
	assert.Contains(t, sess.PullCmd("a.img"), "--s3-version-at 2026-10-01T00:00:00Z")
	assert.NotContains(t, sess.PushCmd("a.img", 1), "version-at", "rclone refuses writes with it set; writes never get it")

	sess.SetReadAt(time.Time{})
	assert.NotContains(t, sess.PullCmd("a.img"), "version-at")
}

func TestParseRcloneLock(t *testing.T) {
	mode, until, err := parseRcloneLock(`NOTICE: something
[{"Path":"a.img","Size":3,"Metadata":{"object-lock-mode":"COMPLIANCE","object-lock-retain-until-date":"2026-11-03T04:05:06Z"}}]`)
	require.NoError(t, err)
	assert.Equal(t, LockCompliance, mode)
	assert.Equal(t, time.Date(2026, 11, 3, 4, 5, 6, 0, time.UTC), until.UTC())

	mode, _, err = parseRcloneLock(`[{"Path":"a.img","Size":3,"Metadata":{"content-type":"x"}}]`)
	require.NoError(t, err)
	assert.Equal(t, LockNone, mode, "an object without lock metadata is unlocked")

	_, _, err = parseRcloneLock(`[]`)
	assert.Error(t, err, "a missing object is not an unlocked one")
}
