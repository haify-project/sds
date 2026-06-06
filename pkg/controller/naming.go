package controller

import (
	"fmt"
	"strconv"
	"strings"
)

const managedNamePrefix = "sds_"

func normalizeManagedName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.HasPrefix(name, managedNamePrefix) {
		return name
	}
	return managedNamePrefix + name
}

func normalizeLVMPoolType(poolType string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(poolType)) {
	case "", "vg", "lvm":
		return "vg", nil
	case "lvm-thin", "thin-pool", "thin_pool":
		return "thin_pool", nil
	default:
		return "", fmt.Errorf("unsupported LVM pool type: %s", poolType)
	}
}

func normalizeManagedZFSPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}

	base := path
	suffix := ""
	if at := strings.Index(path, "@"); at >= 0 {
		base = path[:at]
		suffix = path[at:]
	}

	parts := strings.Split(base, "/")
	if len(parts) == 0 {
		return normalizeManagedName(base) + suffix
	}

	parts[0] = normalizeManagedName(parts[0])
	return strings.Join(parts, "/") + suffix
}

func parseByteCount(raw string) (uint64, error) {
	value := strings.TrimSpace(raw)
	value = strings.TrimSuffix(value, "B")
	value = strings.TrimSuffix(value, "b")
	if value == "" {
		return 0, fmt.Errorf("empty byte count")
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, err
	}
	if parsed < 0 {
		return 0, fmt.Errorf("negative byte count: %s", raw)
	}

	return uint64(parsed), nil
}

func parseLVMPoolLine(line string) (string, uint64, uint64, bool) {
	fields := strings.Split(strings.TrimSpace(line), "|")
	if len(fields) < 3 {
		return "", 0, 0, false
	}

	name := strings.TrimSpace(fields[0])
	if name == "" {
		return "", 0, 0, false
	}

	total, err := parseByteCount(fields[1])
	if err != nil {
		return "", 0, 0, false
	}

	free, err := parseByteCount(fields[2])
	if err != nil {
		return "", 0, 0, false
	}

	return name, total, free, true
}

func parseZFSSnapshotLine(line string) (string, string, string, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 4 {
		return "", "", "", false
	}

	parts := strings.SplitN(fields[0], "@", 2)
	if len(parts) != 2 {
		return "", "", "", false
	}

	return parts[1], parts[0], fields[3], true
}
