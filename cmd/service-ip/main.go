//go:build linux

// Command service-ip brings a floating service IP up or down on the node it
// runs on and announces it with gratuitous ARP. drbd-reactor starts it through
// the service-ip@.service template for Haify HA configs and for the controller's
// own VIP. It ships with Haify so the VIP never depends on something installed
// separately by hand.
package main

func main() {
	Execute()
}
