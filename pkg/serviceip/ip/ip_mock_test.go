//go:build linux

package ip

import (
	"fmt"
	"net"
	"testing"

	"github.com/vishvananda/netlink"
)

// --- Mock Implementation ---

type MockLink struct {
	attrs netlink.LinkAttrs
	addrs []netlink.Addr
}

func (m *MockLink) Attrs() *netlink.LinkAttrs {
	return &m.attrs
}

func (m *MockLink) Type() string { return "mock" }

type MockNetlink struct {
	links         map[string]*MockLink
	linkListErr   error
	linkByNameErr map[string]error
	addrListErr   map[string]error
	addrDelErr    map[string]error
}

func NewMockNetlink() *MockNetlink {
	return &MockNetlink{
		links:         make(map[string]*MockLink),
		linkByNameErr: make(map[string]error),
		addrListErr:   make(map[string]error),
		addrDelErr:    make(map[string]error),
	}
}

func (m *MockNetlink) AddLink(name string, up bool, ips ...string) {
	var flags net.Flags
	if name == "lo" {
		flags |= net.FlagLoopback
	}

	if up {
		flags |= net.FlagUp
	}

	link := &MockLink{
		attrs: netlink.LinkAttrs{Name: name, Flags: flags},
		addrs: []netlink.Addr{},
	}

	for _, ipStr := range ips {
		addr, _ := netlink.ParseAddr(ipStr)
		link.addrs = append(link.addrs, *addr)
	}
	m.links[name] = link
}

func (m *MockNetlink) LinkByName(name string) (netlink.Link, error) {
	if err, ok := m.linkByNameErr[name]; ok {
		return nil, err
	}
	if link, ok := m.links[name]; ok {
		return link, nil
	}
	return nil, fmt.Errorf("Link not found")
}

func (m *MockNetlink) LinkList() ([]netlink.Link, error) {
	if m.linkListErr != nil {
		return nil, m.linkListErr
	}
	var list []netlink.Link
	for _, l := range m.links {
		list = append(list, l)
	}
	return list, nil
}

func (m *MockNetlink) AddrList(link netlink.Link, family int) ([]netlink.Addr, error) {
	if err, ok := m.addrListErr[link.Attrs().Name]; ok {
		return nil, err
	}
	mockLink := m.links[link.Attrs().Name]
	return mockLink.addrs, nil
}

func (m *MockNetlink) AddrAdd(link netlink.Link, addr *netlink.Addr) error {
	mockLink := m.links[link.Attrs().Name]
	// Check duplicate (simple check)
	for _, a := range mockLink.addrs {
		if a.IP.Equal(addr.IP) {
			return fmt.Errorf("file exists")
		}
	}
	mockLink.addrs = append(mockLink.addrs, *addr)
	return nil
}

func (m *MockNetlink) AddrDel(link netlink.Link, addr *netlink.Addr) error {
	if err, ok := m.addrDelErr[link.Attrs().Name]; ok {
		return err
	}
	mockLink := m.links[link.Attrs().Name]
	newAddrs := []netlink.Addr{}
	found := false
	for _, a := range mockLink.addrs {
		if a.IP.Equal(addr.IP) {
			found = true
			continue
		}
		newAddrs = append(newAddrs, a)
	}
	if !found {
		return fmt.Errorf("cannot assign requested address")
	}
	mockLink.addrs = newAddrs
	return nil
}

func (m *MockNetlink) SetLinkByNameError(name string, err error) {
	m.linkByNameErr[name] = err
}

func (m *MockNetlink) SetAddrListError(name string, err error) {
	m.addrListErr[name] = err
}

func (m *MockNetlink) SetAddrDelError(name string, err error) {
	m.addrDelErr[name] = err
}

// --- Tests ---

func TestEnsureIP_Success(t *testing.T) {
	mock := NewMockNetlink()
	mock.AddLink("eth0", true, "192.168.1.10/24")
	SetProvider(mock)

	// Action: Add new IP
	dev, err := EnsureIP("eth0", "192.168.1.200/24")
	if err != nil {
		t.Fatalf("EnsureIP failed: %v", err)
	}
	if dev != "eth0" {
		t.Errorf("Expected dev eth0, got %s", dev)
	}

	// Verify
	exists, _ := CheckIP("eth0", "192.168.1.200/24")
	if !exists {
		t.Error("IP was not added to mock")
	}
}

func TestEnsureIP_Idempotent(t *testing.T) {
	mock := NewMockNetlink()
	mock.AddLink("eth0", true, "192.168.1.200/24") // IP already exists
	SetProvider(mock)

	// Action: Add same IP
	dev, err := EnsureIP("eth0", "192.168.1.200/24")
	if err != nil {
		t.Fatalf("EnsureIP failed on existing IP: %v", err)
	}
	if dev != "eth0" {
		t.Errorf("Expected dev eth0, got %s", dev)
	}
}

func TestEnsureIP_InterfaceDown(t *testing.T) {
	mock := NewMockNetlink()
	mock.AddLink("eth0", false) // Down
	SetProvider(mock)

	_, err := EnsureIP("eth0", "192.168.1.200/24")
	if err == nil {
		t.Error("Expected error when interface is DOWN, got nil")
	}
}

func TestRemoveIP_Success(t *testing.T) {
	mock := NewMockNetlink()
	mock.AddLink("eth0", true, "192.168.1.200/24")
	SetProvider(mock)

	// Action: Remove IP
	err := RemoveIP("eth0", "192.168.1.200/24")
	if err != nil {
		t.Fatalf("RemoveIP failed: %v", err)
	}

	// Verify
	exists, _ := CheckIP("eth0", "192.168.1.200/24")
	if exists {
		t.Error("IP was not removed from mock")
	}
}

func TestAutoDetectInterface(t *testing.T) {
	mock := NewMockNetlink()
	mock.AddLink("eth0", true, "10.0.0.1/8")
	mock.AddLink("eth1", true, "192.168.10.5/24")
	SetProvider(mock)

	// Should match eth1 because 192.168.10.200 is in 192.168.10.0/24
	dev, err := EnsureIP("", "192.168.10.200/24")
	if err != nil {
		t.Fatalf("EnsureIP auto-detect failed: %v", err)
	}
	if dev != "eth1" {
		t.Errorf("Expected auto-detect eth1, got %s", dev)
	}
}

func TestAutoDetectInterface_BroaderInterfacePrefix(t *testing.T) {
	mock := NewMockNetlink()
	mock.AddLink("eth0", true, "10.0.0.1/8")
	SetProvider(mock)

	dev, err := EnsureIP("", "10.1.2.3/24")
	if err != nil {
		t.Fatalf("EnsureIP auto-detect with broader prefix failed: %v", err)
	}
	if dev != "eth0" {
		t.Errorf("Expected auto-detect eth0, got %s", dev)
	}
}

func TestAutoDetectInterface_HostRouteVIP(t *testing.T) {
	mock := NewMockNetlink()
	mock.AddLink("eth0", true, "192.168.10.5/24")
	SetProvider(mock)

	dev, err := EnsureIP("", "192.168.10.200/32")
	if err != nil {
		t.Fatalf("EnsureIP auto-detect with /32 VIP failed: %v", err)
	}
	if dev != "eth0" {
		t.Errorf("Expected auto-detect eth0, got %s", dev)
	}
}

func TestRemoveIP_PropagatesAddrListError(t *testing.T) {
	mock := NewMockNetlink()
	mock.AddLink("eth0", true, "192.168.1.200/24")
	mock.SetAddrListError("eth0", fmt.Errorf("addr list failed"))
	SetProvider(mock)

	err := RemoveIP("eth0", "192.168.1.200/24")
	if err == nil {
		t.Fatal("expected RemoveIP to return error when address inspection fails")
	}
}

func TestRemoveIP_ReturnsNilWhenIPAlreadyGone(t *testing.T) {
	mock := NewMockNetlink()
	mock.AddLink("eth0", true, "192.168.1.10/24")
	SetProvider(mock)

	err := RemoveIP("", "192.168.1.200/24")
	if err != nil {
		t.Fatalf("expected nil when IP is already gone, got %v", err)
	}
}

func TestRemoveIP_PropagatesScanErrorDuringAutoDetect(t *testing.T) {
	mock := NewMockNetlink()
	mock.AddLink("eth0", true, "192.168.1.10/24")
	mock.SetAddrListError("eth0", fmt.Errorf("addr list failed"))
	SetProvider(mock)

	err := RemoveIP("", "192.168.1.200/24")
	if err == nil {
		t.Fatal("expected RemoveIP to return error when auto-detect scan fails")
	}
}

func TestCheckIP_PropagatesScanErrorWhenStateUnknown(t *testing.T) {
	mock := NewMockNetlink()
	mock.AddLink("eth0", true, "192.168.1.10/24")
	mock.SetAddrListError("eth0", fmt.Errorf("addr list failed"))
	SetProvider(mock)

	_, err := CheckIP("", "192.168.1.200/24")
	if err == nil {
		t.Fatal("expected CheckIP to return error when scan fails and IP is not found")
	}
}

func TestCheckIP_FindsIPDespiteOtherScanErrors(t *testing.T) {
	mock := NewMockNetlink()
	mock.AddLink("eth0", true, "192.168.1.10/24")
	mock.AddLink("eth1", true, "192.168.1.200/24")
	mock.SetAddrListError("eth0", fmt.Errorf("addr list failed"))
	SetProvider(mock)

	exists, err := CheckIP("", "192.168.1.200/24")
	if err != nil {
		t.Fatalf("expected CheckIP to succeed when another interface has the IP, got %v", err)
	}
	if !exists {
		t.Fatal("expected CheckIP to find the target IP")
	}
}
