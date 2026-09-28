//go:build linux

package ip

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"syscall"

	"github.com/vishvananda/netlink"
)

// NetlinkProvider defines the interface for interacting with the kernel.
// This allows us to mock netlink calls for unit testing.
type NetlinkProvider interface {
	LinkByName(name string) (netlink.Link, error)
	LinkList() ([]netlink.Link, error)
	AddrList(link netlink.Link, family int) ([]netlink.Addr, error)
	AddrAdd(link netlink.Link, addr *netlink.Addr) error
	AddrDel(link netlink.Link, addr *netlink.Addr) error
}

// RealNetlink is the actual implementation using the netlink library.
type RealNetlink struct{}

func (r *RealNetlink) LinkByName(name string) (netlink.Link, error) { return netlink.LinkByName(name) }
func (r *RealNetlink) LinkList() ([]netlink.Link, error)            { return netlink.LinkList() }
func (r *RealNetlink) AddrList(link netlink.Link, family int) ([]netlink.Addr, error) {
	return netlink.AddrList(link, family)
}
func (r *RealNetlink) AddrAdd(link netlink.Link, addr *netlink.Addr) error {
	return netlink.AddrAdd(link, addr)
}
func (r *RealNetlink) AddrDel(link netlink.Link, addr *netlink.Addr) error {
	return netlink.AddrDel(link, addr)
}

// provider holds the current implementation. Defaults to RealNetlink.
var provider NetlinkProvider = &RealNetlink{}

var errIPNotFound = errors.New("ip not found")

// SetProvider allows tests to inject a mock provider.
func SetProvider(p NetlinkProvider) {
	provider = p
}

// EnsureIP ensures that the given IP/CIDR is present on the interface.
func EnsureIP(ifaceName, ipCIDR string) (string, error) {
	addr, err := netlink.ParseAddr(ipCIDR)
	if err != nil {
		return "", fmt.Errorf("invalid CIDR %q: %w", ipCIDR, err)
	}

	if ifaceName == "" {
		ifaceName, err = FindInterfaceForCIDR(addr.IPNet)
		if err != nil {
			return "", err
		}
	}

	link, err := provider.LinkByName(ifaceName)
	if err != nil {
		return "", fmt.Errorf("interface %q not found: %w", ifaceName, err)
	}

	// Ensure interface is UP
	if link.Attrs().Flags&net.FlagUp == 0 {
		return "", fmt.Errorf("interface %q is DOWN, cannot add IP", ifaceName)
	}

	exists, err := hasIP(link, addr)
	if err != nil {
		return "", err
	}

	if exists {
		slog.Debug("IP already present, skipping add", "ip", addr.IP.String(), "dev", ifaceName)
		return ifaceName, nil
	}

	if err := provider.AddrAdd(link, addr); err != nil {
		return "", fmt.Errorf("failed to add address %s to %s: %w", ipCIDR, ifaceName, err)
	}

	return ifaceName, nil
}

// RemoveIP ensures the IP is gone.
func RemoveIP(ifaceName, ipCIDR string) error {
	addr, err := netlink.ParseAddr(ipCIDR)
	if err != nil {
		return fmt.Errorf("invalid CIDR %q: %w", ipCIDR, err)
	}

	if ifaceName == "" {
		ifaceName, err = FindInterfaceHoldingIP(addr)
		if err != nil {
			if errors.Is(err, errIPNotFound) {
				return nil
			}
			return err
		}
	}

	link, err := provider.LinkByName(ifaceName)
	if err != nil {
		return fmt.Errorf("interface %q not found: %w", ifaceName, err)
	}

	exists, err := hasIP(link, addr)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	if err := provider.AddrDel(link, addr); err != nil {
		if isAddrNotAvailable(err) {
			return nil
		}
		return fmt.Errorf("failed to delete address %s from %s: %w", ipCIDR, ifaceName, err)
	}

	return nil
}

// CheckIP for monitor.
func CheckIP(ifaceName, ipCIDR string) (bool, error) {
	addr, err := netlink.ParseAddr(ipCIDR)
	if err != nil {
		return false, err
	}

	if ifaceName != "" {
		link, err := provider.LinkByName(ifaceName)
		if err != nil {
			return false, err
		}
		return hasIP(link, addr)
	}

	links, err := provider.LinkList()
	if err != nil {
		return false, err
	}
	var firstErr error
	for _, link := range links {
		exists, err := hasIP(link, addr)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("failed to inspect interface %q: %w", link.Attrs().Name, err)
			}
			continue
		}
		if exists {
			return true, nil
		}
	}
	if firstErr != nil {
		return false, firstErr
	}
	return false, nil
}

func hasIP(link netlink.Link, target *netlink.Addr) (bool, error) {
	addrs, err := provider.AddrList(link, familyForIP(target.IP))
	if err != nil {
		return false, err
	}

	for _, addr := range addrs {
		if addr.IP.Equal(target.IP) && addr.Mask.String() == target.Mask.String() {
			return true, nil
		}
	}
	return false, nil
}

// FindInterfaceForCIDR looks for an interface in the same subnet.
func FindInterfaceForCIDR(targetNet *net.IPNet) (string, error) {
	links, err := provider.LinkList()
	if err != nil {
		return "", err
	}

	var firstErr error
	for _, link := range links {
		if link.Attrs().Flags&net.FlagLoopback != 0 || link.Attrs().Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := provider.AddrList(link, familyForIP(targetNet.IP))
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("failed to inspect interface %q: %w", link.Attrs().Name, err)
			}
			continue
		}

		for _, addr := range addrs {
			if !sameIPFamily(targetNet.IP, addr.IP) || addr.IPNet == nil {
				continue
			}
			if addr.Contains(targetNet.IP) {
				return link.Attrs().Name, nil
			}
		}
	}
	if firstErr != nil {
		return "", firstErr
	}
	return "", fmt.Errorf("no interface found for subnet %s", targetNet.String())
}

// FindInterfaceHoldingIP finds which interface has this exact IP.
func FindInterfaceHoldingIP(target *netlink.Addr) (string, error) {
	links, err := provider.LinkList()
	if err != nil {
		return "", err
	}
	var firstErr error
	for _, link := range links {
		exists, err := hasIP(link, target)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("failed to inspect interface %q: %w", link.Attrs().Name, err)
			}
			continue
		}
		if exists {
			return link.Attrs().Name, nil
		}
	}
	if firstErr != nil {
		return "", firstErr
	}
	return "", errIPNotFound
}

func familyForIP(ip net.IP) int {
	if ip.To4() != nil {
		return netlink.FAMILY_V4
	}
	if ip.To16() != nil {
		return netlink.FAMILY_V6
	}
	return netlink.FAMILY_ALL
}

func sameIPFamily(a, b net.IP) bool {
	if a == nil || b == nil {
		return false
	}
	return (a.To4() != nil) == (b.To4() != nil)
}

func isAddrNotAvailable(err error) bool {
	return errors.Is(err, syscall.EADDRNOTAVAIL)
}
