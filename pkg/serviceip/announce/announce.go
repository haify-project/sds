package announce

import (
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/j-keck/arping"
)

// Announcer defines the interface for sending network announcements.
type Announcer interface {
	GratuitousArp(ip net.IP, ifaceName string) error
}

// RealAnnouncer implements Announcer using the arping library.
type RealAnnouncer struct{}

func (r *RealAnnouncer) GratuitousArp(ip net.IP, ifaceName string) error {
	return arping.GratuitousArpOverIfaceByName(ip, ifaceName)
}

var announcer Announcer = &RealAnnouncer{}

// SetAnnouncer allows tests to inject a mock announcer.
func SetAnnouncer(a Announcer) {
	announcer = a
}

// Announce handles GARP for IPv4. IPv6 currently returns an explicit error
// until Neighbor Advertisement support is implemented.
func Announce(ifaceName, ipStr string, count int) error {
	ip, _, err := net.ParseCIDR(ipStr)
	if err != nil {
		ip = net.ParseIP(ipStr)
		if ip == nil {
			return fmt.Errorf("invalid IP %q", ipStr)
		}
	}

	if ip.To4() != nil {
		return announceIPv4(ifaceName, ip, count)
	}

	return fmt.Errorf("IPv6 announcement for %s on %s is not implemented", ip.String(), ifaceName)
}

func announceIPv4(ifaceName string, ip net.IP, count int) error {
	slog.Info("Sending Gratuitous ARP", "ip", ip.String(), "dev", ifaceName, "count", count)
	for i := 0; i < count; i++ {
		if err := announcer.GratuitousArp(ip, ifaceName); err != nil {
			return fmt.Errorf("GARP failed: %w", err)
		}
		if i < count-1 {
			time.Sleep(10 * time.Millisecond) // Reduced sleep for tests
		}
	}
	return nil
}
