package controller

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
)

// resolveToIP resolves a hostname to an IP address. If the input is already
// an IP address, it returns it unchanged. If resolution fails or returns a
// loopback address, it tries to find a non-loopback IP from network interfaces.
func resolveToIP(host string) string {
	// Check if it's already an IP address
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() {
			return host
		}
		// If it's a loopback IP, try to find a real IP
		return getFirstNonLoopbackIP()
	}

	// Try to resolve hostname to IP
	addrs, err := net.LookupHost(host)
	if err != nil || len(addrs) == 0 {
		return getFirstNonLoopbackIP()
	}

	// Prefer non-loopback IPv4 addresses
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip != nil && ip.To4() != nil && !ip.IsLoopback() {
			return addr
		}
	}

	// Fall back to first non-loopback address
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip != nil && !ip.IsLoopback() {
			return addr
		}
	}

	// If all resolved addresses are loopback, get IP from interfaces
	return getFirstNonLoopbackIP()
}

// getFirstNonLoopbackIP returns the first non-loopback IPv4 address from network interfaces
func getFirstNonLoopbackIP() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}

	for _, iface := range interfaces {
		// Skip loopback and down interfaces
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			if ip != nil && ip.To4() != nil && !ip.IsLoopback() {
				return ip.String()
			}
		}
	}

	return "127.0.0.1"
}

func localHostname() string {
	h, _ := os.Hostname()
	return h
}

// getNodeHost gets the host address for a node name
// hosts format is "nodename:ip" or just "nodename"
func (rm *ResourceManager) getNodeHost(nodeName string) string {
	if resolved := rm.controller.ResolveHost(nodeName); resolved != nodeName {
		return resolved
	}

	rm.mu.RLock()
	defer rm.mu.RUnlock()

	for _, host := range rm.hosts {
		// Check if host matches nodename:ip format
		if strings.Contains(host, ":") {
			parts := strings.SplitN(host, ":", 2)
			if parts[0] == nodeName {
				return host
			}
		} else if host == nodeName {
			return host
		}
	}
	return ""
}

// CreateFilesystemOnly creates a filesystem on a DRBD device
// primaryAddress returns the address of the node currently Primary for the
// resource, from live DRBD status.
func (rm *ResourceManager) primaryAddress(ctx context.Context, resource string) (string, error) {
	info, err := rm.GetResource(ctx, resource)
	if err != nil {
		return "", err
	}
	for addr, st := range info.NodeStates {
		if st.Role == "Primary" {
			return addr, nil
		}
	}
	return "", fmt.Errorf("no Primary node for resource %q; specify a node explicitly", resource)
}

// resolveNodeOrPrimary resolves a node selection to a host address. An empty
// selection ("Auto (Primary)") resolves to the current Primary; if there is no
// Primary that is a hard error rather than a silent no-op on an empty host.
func (rm *ResourceManager) resolveNodeOrPrimary(ctx context.Context, resource, node string) (string, error) {
	if strings.TrimSpace(node) == "" {
		return rm.primaryAddress(ctx, resource)
	}
	addr := rm.controller.ResolveHost(node)
	if strings.TrimSpace(addr) == "" {
		return "", fmt.Errorf("unknown node %q", node)
	}
	return addr, nil
}
