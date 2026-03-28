package cmd

import "testing"

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		input int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{int64(1.5 * 1024 * 1024), "1.5 MiB"},
		{1024 * 1024 * 1024, "1.0 GiB"},
		{int64(2.5 * 1024 * 1024 * 1024), "2.5 GiB"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := humanBytes(tc.input); got != tc.want {
				t.Errorf("humanBytes(%d) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
