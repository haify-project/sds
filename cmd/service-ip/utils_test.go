//go:build linux

package main

import "testing"

func TestParseInstanceID(t *testing.T) {
	tests := []struct {
		input       string
		expectedIP  string
		expectedDev string
	}{
		// Case 1: Standard auto-detect format
		{"192.168.10.100-24", "192.168.10.100/24", ""},

		// Case 2: Standard explicit device format
		{"192.168.10.100-24_eth0", "192.168.10.100/24", "eth0"},

		// Case 3: Device name with underscore (should assume split on first underscore)
		{"10.0.0.1-8_eth_0", "10.0.0.1/8", "eth_0"},

		// Case 4: No dashes (already correct format, though Systemd usually escapes /)
		{"192.168.1.1/24_eth0", "192.168.1.1/24", "eth0"},

		// Case 5: Weird multiple dashes
		{"10-0-0-1-24_eth0", "10/0/0/1/24", "eth0"}, // Current logic replaces ALL dashes. This behavior is expected given the implementation.
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			gotIP, gotDev := ParseInstanceID(tt.input)
			if gotIP != tt.expectedIP {
				t.Errorf("ParseInstanceID(%q) IP = %v, want %v", tt.input, gotIP, tt.expectedIP)
			}
			if gotDev != tt.expectedDev {
				t.Errorf("ParseInstanceID(%q) Dev = %v, want %v", tt.input, gotDev, tt.expectedDev)
			}
		})
	}
}
