package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseSize(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected uint64
		hasError bool
	}{
		// Pure numbers (bytes)
		{name: "pure number", input: "1024", expected: 1024, hasError: false},
		{name: "zero", input: "0", expected: 0, hasError: false},

		// Bytes
		{name: "bytes", input: "100B", expected: 100, hasError: false},
		{name: "bytes lowercase", input: "100b", expected: 100, hasError: false},

		// Kilobytes
		{name: "KB", input: "1KB", expected: 1000, hasError: false},
		{name: "KiB", input: "1KiB", expected: 1024, hasError: false},
		{name: "K", input: "1K", expected: 1024, hasError: false},
		{name: "K with space", input: "1 K", expected: 1024, hasError: false},
		{name: "KB with space", input: "10 KB", expected: 10000, hasError: false},

		// Megabytes
		{name: "MB", input: "1MB", expected: 1000 * 1000, hasError: false},
		{name: "MiB", input: "1MiB", expected: 1024 * 1024, hasError: false},
		{name: "M", input: "1M", expected: 1024 * 1024, hasError: false},
		{name: "10MB", input: "10MB", expected: 10 * 1000 * 1000, hasError: false},

		// Gigabytes
		{name: "GB", input: "1GB", expected: 1000 * 1000 * 1000, hasError: false},
		{name: "GiB", input: "1GiB", expected: 1024 * 1024 * 1024, hasError: false},
		{name: "G", input: "1G", expected: 1024 * 1024 * 1024, hasError: false},
		{name: "100GB", input: "100GB", expected: 100 * 1000 * 1000 * 1000, hasError: false},

		// Terabytes
		{name: "TB", input: "1TB", expected: 1000 * 1000 * 1000 * 1000, hasError: false},
		{name: "TiB", input: "1TiB", expected: 1024 * 1024 * 1024 * 1024, hasError: false},
		{name: "T", input: "1T", expected: 1024 * 1024 * 1024 * 1024, hasError: false},

		// Petabytes
		{name: "PB", input: "1PB", expected: 1000 * 1000 * 1000 * 1000 * 1000, hasError: false},
		{name: "PiB", input: "1PiB", expected: 1024 * 1024 * 1024 * 1024 * 1024, hasError: false},
		{name: "P", input: "1P", expected: 1024 * 1024 * 1024 * 1024 * 1024, hasError: false},

		// Exabytes
		{name: "EB", input: "1EB", expected: 1000 * 1000 * 1000 * 1000 * 1000 * 1000, hasError: false},
		{name: "EiB", input: "1EiB", expected: 1024 * 1024 * 1024 * 1024 * 1024 * 1024, hasError: false},
		{name: "E", input: "1E", expected: 1024 * 1024 * 1024 * 1024 * 1024 * 1024, hasError: false},

		// Decimal values
		{name: "1.5GB", input: "1.5GB", expected: uint64(1.5 * 1000 * 1000 * 1000), hasError: false},
		{name: "2.5GiB", input: "2.5GiB", expected: uint64(2.5 * 1024 * 1024 * 1024), hasError: false},

		// With whitespace
		{name: "whitespace", input: "  100GB  ", expected: 100 * 1000 * 1000 * 1000, hasError: false},

		// Error cases
		{name: "empty string", input: "", expected: 0, hasError: true},
		{name: "invalid format", input: "abc", expected: 0, hasError: true},
		{name: "invalid unit", input: "100XB", expected: 0, hasError: true},
		{name: "negative", input: "-100", expected: 0, hasError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseSize(tt.input)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result, "ParseSize(%s)", tt.input)
			}
		})
	}
}

func TestBytesToGB(t *testing.T) {
	tests := []struct {
		input    uint64
		expected uint64
	}{
		{0, 0},
		{1, 0},
		{999, 0},
		{1000, 0},
		{1000 * 1000, 0},
		{1000 * 1000 * 1000, 1},
		{10 * 1000 * 1000 * 1000, 10},
		{1024 * 1024 * 1024, 1}, // 1 GiB = 1.0737... GB
	}

	for _, tt := range tests {
		result := BytesToGB(tt.input)
		assert.Equal(t, tt.expected, result, "BytesToGB(%d)", tt.input)
	}
}

func TestBytesToGiB(t *testing.T) {
	tests := []struct {
		input    uint64
		expected uint64
	}{
		{0, 0},
		{1, 0},
		{1023, 0},
		{1024, 0},
		{1024 * 1024, 0},
		{1024 * 1024 * 1024, 1},
		{10 * 1024 * 1024 * 1024, 10},
		{1000 * 1000 * 1000, 0}, // 1 GB < 1 GiB
	}

	for _, tt := range tests {
		result := BytesToGiB(tt.input)
		assert.Equal(t, tt.expected, result, "BytesToGiB(%d)", tt.input)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input    uint64
		expected string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{100, "100 B"},
		{1024, "1.00 KiB"},
		{1024 * 1024, "1.00 MiB"},
		{1024 * 1024 * 1024, "1.00 GiB"},
		{1024 * 1024 * 1024 * 1024, "1.00 TiB"},
		{1536, "1.50 KiB"},                    // 1.5 KiB
		{2 * 1024 * 1024, "2.00 MiB"},         // 2 MiB
		{3 * 1024 * 1024 * 1024, "3.00 GiB"},  // 3 GiB
		{500 * 1024 * 1024, "500.00 MiB"},     // 500 MiB
	}

	for _, tt := range tests {
		result := FormatBytes(tt.input)
		assert.Equal(t, tt.expected, result, "FormatBytes(%d)", tt.input)
	}
}

func TestSizeToGB(t *testing.T) {
	tests := []struct {
		input    string
		expected uint64
		hasError bool
	}{
		{"1GB", 1, false},
		{"10GB", 10, false},
		{"1GiB", 1, false},
		{"1000MB", 1, false},
		{"1024MiB", 1, false},
		{"1TB", 1000, false},
		{"invalid", 0, true},
	}

	for _, tt := range tests {
		result, err := SizeToGB(tt.input)
		if tt.hasError {
			assert.Error(t, err)
		} else {
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result, "SizeToGB(%s)", tt.input)
		}
	}
}

func TestSizeToGiBString(t *testing.T) {
	tests := []struct {
		input    string
		expected uint64
		hasError bool
	}{
		{"1GiB", 1, false},
		{"10GiB", 10, false},
		{"1GB", 0, false},  // 1GB < 1GiB
		{"1024MiB", 1, false},
		{"1TiB", 1024, false},
		{"invalid", 0, true},
	}

	for _, tt := range tests {
		result, err := SizeToGiBString(tt.input)
		if tt.hasError {
			assert.Error(t, err)
		} else {
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result, "SizeToGiBString(%s)", tt.input)
		}
	}
}
