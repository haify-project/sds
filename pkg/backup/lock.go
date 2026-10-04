package backup

import (
	"fmt"
	"strings"
)

// Immutable backups: S3 Object Lock.
//
// A backup that whoever holds the cluster can delete is not a backup against
// whoever holds the cluster. Ransomware with a stolen sds token, or root on a
// storage node, deletes the backups first and encrypts second. Object Lock
// moves the guarantee to the object store: an object locked until a date
// cannot be deleted or overwritten before it, by sds or anyone using sds's
// credentials (governance mode: anyone without s3:BypassGovernanceRetention;
// compliance mode: anyone at all, the bucket owner included).
//
// The lock is only as strong as the credentials sds is given, and those sit
// on the storage nodes. They must not carry s3:BypassGovernanceRetention,
// s3:DeleteObjectVersion or s3:PutBucketObjectLockConfiguration; with any of
// them the lock is a formality. The user guide has the policy.

// LockMode is an S3 Object Lock retention mode.
type LockMode string

const (
	// LockNone writes objects unlocked.
	LockNone LockMode = ""
	// LockGovernance can be lifted by a principal holding
	// s3:BypassGovernanceRetention, which sds's credentials must not.
	LockGovernance LockMode = "governance"
	// LockCompliance cannot be lifted by anyone before it expires.
	LockCompliance LockMode = "compliance"
)

// DefaultFullEveryDays is how long an incremental chain grows on a locked
// target before the next backup is full, when the target does not say.
const DefaultFullEveryDays = 7

// MinRcloneForLock is the first rclone release that sets Object Lock on upload
// (and computes the Content-MD5 it requires).
const MinRcloneForLock = "1.74.0"

// ParseLockMode validates a lock mode.
func ParseLockMode(s string) (LockMode, error) {
	switch LockMode(strings.ToLower(strings.TrimSpace(s))) {
	case LockNone:
		return LockNone, nil
	case LockGovernance:
		return LockGovernance, nil
	case LockCompliance:
		return LockCompliance, nil
	default:
		return "", fmt.Errorf("backup: unknown lock mode %q (want governance or compliance)", s)
	}
}

// Locked reports whether the target locks what it stores.
func (t TargetSpec) Locked() bool { return t.LockMode != LockNone }

// FullEvery is the chain length on a locked target, in days.
func (t TargetSpec) FullEvery() int {
	if t.FullEveryDays > 0 {
		return t.FullEveryDays
	}
	return DefaultFullEveryDays
}

func (t TargetSpec) validateLock() error {
	if !t.Locked() {
		if t.LockDays != 0 {
			return fmt.Errorf("backup: target %q sets lock days without a lock mode", t.Name)
		}
		return nil
	}
	if _, err := ParseLockMode(string(t.LockMode)); err != nil {
		return err
	}
	if t.Kind != KindS3 {
		return fmt.Errorf("backup: target %q: object lock is an S3 feature; %s targets cannot lock", t.Name, t.Kind)
	}
	if t.LockDays < 1 || t.LockDays > 36500 {
		return fmt.Errorf("backup: target %q: lock days must be between 1 and 36500", t.Name)
	}
	if t.FullEveryDays < 0 || t.FullEveryDays > 365 {
		return fmt.Errorf("backup: target %q: full-every days must be between 1 and 365", t.Name)
	}
	return nil
}

// s3LockMode is the mode as the S3 API and rclone spell it.
func (m LockMode) s3() string { return strings.ToUpper(string(m)) }

// rcloneVersionAtLeast compares the "rclone v1.74.0" line rclone prints with
// a minimum "1.74.0". A development or unparsable version is taken as too old:
// a lock silently not applied is the failure this check exists to prevent.
func rcloneVersionAtLeast(versionLine, min string) bool {
	f := strings.Fields(versionLine)
	if len(f) < 2 {
		return false
	}
	have := parseVersion(strings.TrimPrefix(f[1], "v"))
	want := parseVersion(min)
	if have == nil || want == nil {
		return false
	}
	for i := 0; i < 3; i++ {
		if have[i] != want[i] {
			return have[i] > want[i]
		}
	}
	return true
}

func parseVersion(v string) []int {
	v, _, _ = strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) < 3 {
		return nil
	}
	out := make([]int, 3)
	for i := 0; i < 3; i++ {
		n := 0
		if _, err := fmt.Sscanf(parts[i], "%d", &n); err != nil {
			return nil
		}
		out[i] = n
	}
	return out
}
