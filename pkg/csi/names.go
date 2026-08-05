package csi

// sanitizeResourceName maps a CSI volume name (typically "pvc-<uuid>") to a
// DRBD-safe resource name: only [A-Za-z0-9_], starts with a letter, <=64 chars.
// The result becomes the volumeHandle, so it is stable for the volume's life and
// needs no reverse mapping (Kubernetes persists it in the PV object).
func sanitizeResourceName(in string) string {
	out := make([]rune, 0, len(in))
	for _, r := range in {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 || !isLetter(out[0]) {
		out = append([]rune{'v'}, out...)
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return string(out)
}

// snapshotNamePrefix keeps generated snapshot names out of LVM's reserved
// namespace. lvcreate rejects any LV whose name starts with "snapshot"
// ("Names starting \"snapshot\" are reserved"), and Kubernetes names every
// VolumeSnapshot "snapshot-<uuid>" — so the sanitized name would be rejected on
// the node without this prefix.
const snapshotNamePrefix = "sdssnap_"

// sanitizeSnapshotName maps a CSI snapshot name (typically "snapshot-<uuid>") to
// a name that LVM and ZFS both accept. The result is embedded in the CSI
// snapshot ID, so create and delete always agree on it without a reverse
// mapping.
func sanitizeSnapshotName(in string) string {
	return snapshotNamePrefix + sanitizeResourceName(in)
}

func isLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}
