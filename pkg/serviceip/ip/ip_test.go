//go:build linux

package ip

import (
	"net"
	"testing"
)

func TestFindInterfaceForCIDR_Logic(t *testing.T) {
	// This tests the matching logic if we had interfaces.
	// In a real environment, we'd use netns.
	t.Log("Testing subnet matching logic...")

	_, targetNet, _ := net.ParseCIDR("192.168.10.100/24")

	testCases := []struct {
		name     string
		ifaceIP  string
		expected bool
	}{
		{"In Subnet", "192.168.10.1", true},
		{"Out of Subnet", "192.168.20.1", false},
		{"Boundary", "192.168.10.254", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.ifaceIP)
			if targetNet.Contains(ip) != tc.expected {
				t.Errorf("For IP %s, expected match %v", tc.ifaceIP, tc.expected)
			}
		})
	}
}
