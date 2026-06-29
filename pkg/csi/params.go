package csi

import (
	"fmt"
	"strconv"
)

// VolumeParams is the parsed StorageClass.parameters for CreateVolume.
type VolumeParams struct {
	Pool        string // VG (lvm) or zpool (zfs) name; required
	Replicas    int    // diskful copies; default 2
	StorageType string // "lvm" or "zfs"; default "lvm"
}

// ParseVolumeParams validates and defaults the StorageClass parameters.
func ParseVolumeParams(p map[string]string) (VolumeParams, error) {
	out := VolumeParams{Pool: p["pool"], Replicas: 2, StorageType: "lvm"}
	if out.Pool == "" {
		return out, fmt.Errorf("storageclass parameter \"pool\" is required")
	}
	if v, ok := p["replicas"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return out, fmt.Errorf("invalid replicas %q", v)
		}
		out.Replicas = n
	}
	if v, ok := p["storageType"]; ok {
		if v != "lvm" && v != "zfs" {
			return out, fmt.Errorf("invalid storageType %q (want lvm or zfs)", v)
		}
		out.StorageType = v
	}
	return out, nil
}
