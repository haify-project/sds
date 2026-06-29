package csi

import (
	"os"

	mount "k8s.io/mount-utils"
	utilexec "k8s.io/utils/exec"
)

// Mounter is the node-plugin mount surface; abstracted for testing.
type Mounter interface {
	// FormatAndMount formats source with fsType if needed, then mounts at target.
	FormatAndMount(source, target, fsType string, options []string) error
	// Mount performs a plain mount (used for bind mounts).
	Mount(source, target, fsType string, options []string) error
	// Unmount unmounts target if mounted.
	Unmount(target string) error
	// IsMountPoint reports whether target is currently a mount point.
	IsMountPoint(target string) (bool, error)
	// EnsureDir makes target (and parents) if absent.
	EnsureDir(target string) error
}

type safeMounter struct {
	m *mount.SafeFormatAndMount
}

// NewMounter returns the production Mounter backed by k8s.io/mount-utils.
func NewMounter() Mounter {
	return &safeMounter{
		m: mount.NewSafeFormatAndMount(mount.New(""), utilexec.New()),
	}
}

func (s *safeMounter) FormatAndMount(source, target, fsType string, options []string) error {
	return s.m.FormatAndMount(source, target, fsType, options)
}

func (s *safeMounter) Mount(source, target, fsType string, options []string) error {
	return s.m.Interface.Mount(source, target, fsType, options)
}

func (s *safeMounter) Unmount(target string) error {
	mounted, err := s.IsMountPoint(target)
	if err != nil || !mounted {
		return nil
	}
	return s.m.Interface.Unmount(target)
}

func (s *safeMounter) IsMountPoint(target string) (bool, error) {
	notMnt, err := s.m.Interface.IsLikelyNotMountPoint(target)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !notMnt, nil
}

func (s *safeMounter) EnsureDir(target string) error {
	return os.MkdirAll(target, 0o750)
}
