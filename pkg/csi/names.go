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

func isLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}
