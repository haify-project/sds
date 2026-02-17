package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSizeEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected uint64
		hasError bool
	}{
		// Decimal values
		{"1.5GB", "1.5GB", 1500 * 1000 * 1000, false},
		{"2.5GiB", "2.5GiB", uint64(2.5 * 1024 * 1024 * 1024), false},

		// Whitespace handling
		{"leading whitespace", "  100GB", 100 * 1000 * 1000 * 1000, false},
		{"trailing whitespace", "100GB  ", 100 * 1000 * 1000 * 1000, false},
		{"both whitespace", "  100GB  ", 100 * 1000 * 1000 * 1000, false},
		{"tab", "\t100GB\t", 100 * 1000 * 1000 * 1000, false},

		// Zero
		{"zero bytes", "0", 0, false},
		{"zero KB", "0KB", 0, false},
		{"zero GB", "0GB", 0, false},

		// Large values
		{"1PB", "1PB", 1000 * 1000 * 1000 * 1000 * 1000, false},
		{"1PiB", "1PiB", 1024 * 1024 * 1024 * 1024 * 1024, false},
		{"1EB", "1EB", 1000 * 1000 * 1000 * 1000 * 1000 * 1000, false},
		{"1EiB", "1EiB", 1024 * 1024 * 1024 * 1024 * 1024 * 1024, false},

		// Error cases
		{"empty string", "", 0, true},
		{"spaces only", "   ", 0, true},
		{"invalid number", "abcGB", 0, true},
		{"invalid suffix", "100XB", 0, true},
		{"negative", "-100GB", 0, true},
		{"negative float", "-1.5GB", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseSize(tt.input)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result, "ParseSize(%s)", tt.input)
			}
		})
	}
}

func TestBytesToGBEdgeCases(t *testing.T) {
	tests := []struct {
		input    uint64
		expected uint64
	}{
		{0, 0},
		{1, 0},
		{500, 0},
		{999, 0},
		{1000, 0},
		{1000 * 1000, 0},
		{1000 * 1000 * 1000, 1},
		{1500 * 1000 * 1000, 1},
		{2000 * 1000 * 1000, 2},
		{1024 * 1024 * 1024, 1}, // 1 GiB is about 1.07 GB
		{10 * 1000 * 1000 * 1000, 10},
		{100 * 1000 * 1000 * 1000, 100},
		{1000 * 1000 * 1000 * 1000, 1000},
	}

	for _, tt := range tests {
		result := BytesToGB(tt.input)
		assert.Equal(t, tt.expected, result, "BytesToGB(%d)", tt.input)
	}
}

func TestBytesToGiBEdgeCases(t *testing.T) {
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
		{2 * 1024 * 1024 * 1024, 2},
		{10 * 1024 * 1024 * 1024, 10},
		{100 * 1024 * 1024 * 1024, 100},
		{1000 * 1024 * 1024 * 1024, 1000},
		{1024 * 1024 * 1024 * 1024, 1024}, // 1 TiB
	}

	for _, tt := range tests {
		result := BytesToGiB(tt.input)
		assert.Equal(t, tt.expected, result, "BytesToGiB(%d)", tt.input)
	}
}

func TestFormatBytesEdgeCases(t *testing.T) {
	tests := []struct {
		input    uint64
		expected string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.00 KiB"},
		{1536, "1.50 KiB"},
		{2048, "2.00 KiB"},
		{1024 * 1024, "1.00 MiB"},
		{1.5 * 1024 * 1024, "1.50 MiB"},
		{1024 * 1024 * 1024, "1.00 GiB"},
		{2.5 * 1024 * 1024 * 1024, "2.50 GiB"},
		{1024 * 1024 * 1024 * 1024, "1.00 TiB"},
		{1024 * 1024 * 1024 * 1024 * 1024, "1.00 PiB"},
		{1024 * 1024 * 1024 * 1024 * 1024 * 1024, "1.00 EiB"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := FormatBytes(uint64(tt.input))
			assert.Equal(t, tt.expected, result, "FormatBytes(%d)", tt.input)
		})
	}
}

func TestSizeToGBEdgeCases(t *testing.T) {
	tests := []struct {
		input    string
		expected uint64
		hasError bool
	}{
		{"1GB", 1, false},
		{"100GB", 100, false},
		{"1000GB", 1000, false},
		{"1TB", 1000, false},
		{"1TiB", 1099, false}, // 1 TiB ≈ 1099 GB
		{"1000MB", 1, false},
		{"1024MB", 1, false},
		{"1PB", 1000000, false},
		{"invalid", 0, true},
		{"", 0, true},
		{"-1GB", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := SizeToGB(tt.input)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result, "SizeToGB(%s)", tt.input)
			}
		})
	}
}

func TestSizeToGiBStringEdgeCases(t *testing.T) {
	tests := []struct {
		input    string
		expected uint64
		hasError bool
	}{
		{"1GiB", 1, false},
		{"100GiB", 100, false},
		{"1024GiB", 1024, false},
		{"1TiB", 1024, false},
		{"1TB", 931, false}, // 1TB = 10^12 bytes / 1024^3 ≈ 931 GiB
		{"1000MiB", 0, false},
		{"1024MiB", 1, false},
		{"10GB", 9, false},  // 10GB is about 9.3 GiB
		{"invalid", 0, true},
		{"", 0, true},
		{"-1GiB", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := SizeToGiBString(tt.input)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result, "SizeToGiBString(%s)", tt.input)
			}
		})
	}
}
