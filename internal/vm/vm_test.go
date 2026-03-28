package vm

import (
	"testing"
)

func TestParseMemoryMB(t *testing.T) {
	tests := []struct {
		input string
		want  uint64
	}{
		{"2G", 2048},
		{"512M", 512},
		{"1G", 1024},
		{"4G", 4096},
		{"256M", 256},
	}
	for _, tt := range tests {
		got, err := parseMemoryMB(tt.input)
		if err != nil {
			t.Errorf("parseMemoryMB(%q): %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseMemoryMB(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseMemoryMB_Invalid(t *testing.T) {
	cases := []string{"2T", "abc", ""}
	for _, c := range cases {
		_, err := parseMemoryMB(c)
		if err == nil {
			t.Errorf("parseMemoryMB(%q) should return error", c)
		}
	}
}

func TestSanitizeTag(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"/workspace", "workspace"},
		{"/mnt/data", "mnt_data"},
		{"share", "share"},
		{"/", "share"}, // becomes empty after trim, falls back to "share"
	}
	for _, tt := range tests {
		got := sanitizeTag(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeTag(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestHashConfig(t *testing.T) {
	hash := hashConfig("/nonexistent/path")
	if hash != "" {
		t.Errorf("hashConfig for nonexistent file should be empty, got %q", hash)
	}
}
