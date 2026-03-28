package provisioner

import (
	"bytes"
	"strings"
	"testing"

	"github.com/3clabs/nova/internal/config"
)

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"hello", "'hello'"},
		{"", "''"},
		{"it's here", "'it'\"'\"'s here'"},
		{"no spaces", "'no spaces'"},
		{"a'b'c", "'a'\"'\"'b'\"'\"'c'"},
	}
	for _, tt := range tests {
		got := shellQuote(tt.input)
		if got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestOutputWriter(t *testing.T) {
	var buf bytes.Buffer
	w := &OutputWriter{Prefix: "web", Writer: &buf}

	w.Write([]byte("line one\nline two\n"))

	output := buf.String()
	if !strings.Contains(output, "[web] line one") {
		t.Errorf("output should contain prefixed line one, got: %q", output)
	}
	if !strings.Contains(output, "[web] line two") {
		t.Errorf("output should contain prefixed line two, got: %q", output)
	}
}

func TestOutputWriter_SingleLine(t *testing.T) {
	var buf bytes.Buffer
	w := &OutputWriter{Prefix: "db", Writer: &buf}

	w.Write([]byte("hello world"))

	output := buf.String()
	if output != "[db] hello world\n" {
		t.Errorf("output = %q, want [db] hello world\\n", output)
	}
}

func TestRunAll_Empty(t *testing.T) {
	// RunAll with no provisioners should be a no-op.
	var buf bytes.Buffer
	err := RunAll(t.Context(), SSHConfig{}, nil, &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output, got %q", buf.String())
	}
}

func TestIsValidEnvKey(t *testing.T) {
	valid := []string{"FOO", "BAR_BAZ", "a", "MY_VAR_2", "_PRIVATE", "lowercase"}
	for _, k := range valid {
		if !isValidEnvKey(k) {
			t.Errorf("isValidEnvKey(%q) should be true", k)
		}
	}

	invalid := []string{"", "1BAD", "FOO=BAR", "has space", "semi;colon", "FOO-BAR", "a.b"}
	for _, k := range invalid {
		if isValidEnvKey(k) {
			t.Errorf("isValidEnvKey(%q) should be false", k)
		}
	}
}

func TestRunAll_InvalidTimeout(t *testing.T) {
	var buf bytes.Buffer
	provs := []config.Provisioner{
		{Type: "shell", Inline: []string{"echo hi"}, Timeout: "not-valid"},
	}
	err := RunAll(t.Context(), SSHConfig{Host: "127.0.0.1", Port: "22"}, provs, &buf)
	if err == nil {
		t.Fatal("expected error for invalid timeout")
	}
	if !strings.Contains(err.Error(), "invalid timeout") {
		t.Errorf("error = %q, should mention invalid timeout", err)
	}
}
