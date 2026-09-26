package announce

import (
	"net"
	"testing"
)

type MockAnnouncer struct {
	CallCount int
	LastIP    net.IP
	LastIface string
}

func (m *MockAnnouncer) GratuitousArp(ip net.IP, ifaceName string) error {
	m.CallCount++
	m.LastIP = ip
	m.LastIface = ifaceName
	return nil
}

func TestAnnounceIPv4(t *testing.T) {
	mock := &MockAnnouncer{}
	SetAnnouncer(mock)

	targetIP := "192.168.1.100/24"
	targetIface := "eth0"
	count := 3

	err := Announce(targetIface, targetIP, count)
	if err != nil {
		t.Fatalf("Announce failed: %v", err)
	}

	if mock.CallCount != count {
		t.Errorf("Expected %d calls, got %d", count, mock.CallCount)
	}

	// Should extract the IP correctly from CIDR
	expectedIP := net.ParseIP("192.168.1.100")
	if !mock.LastIP.Equal(expectedIP) {
		t.Errorf("Expected IP %v, got %v", expectedIP, mock.LastIP)
	}

	if mock.LastIface != targetIface {
		t.Errorf("Expected iface %s, got %s", targetIface, mock.LastIface)
	}
}

func TestAnnounceInvalidIP(t *testing.T) {
	err := Announce("eth0", "invalid-ip", 1)
	if err == nil {
		t.Error("Expected error for invalid IP, got nil")
	}
}

func TestAnnounceIPv6NotImplemented(t *testing.T) {
	err := Announce("eth0", "2001:db8::100/64", 1)
	if err == nil {
		t.Fatal("expected IPv6 announcement to fail until NDP support exists")
	}
}
