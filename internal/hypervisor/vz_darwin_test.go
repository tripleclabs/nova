//go:build darwin

package hypervisor

import "testing"

func TestParseDHCPLeases_FindsMAC(t *testing.T) {
	leases := `{
	name=my-vm
	ip_address=192.168.64.3
	hw_address=ff,0:0:0:2:0:1:0:1:31:5a:ff:52:52:54:0:0:0:2
	identifier=ff,0:0:0:2:0:1:0:1:31:5a:ff:52:52:54:0:0:0:2
	lease=0x69c850e4
}
{
	ip_address=192.168.64.2
	hw_address=1,16:c4:cf:99:8c:5d
	identifier=1,16:c4:cf:99:8c:5d
	lease=0x673338be
}
`
	ip, err := ParseDHCPLeases(leases, "52:54:00:00:00:02")
	if err != nil {
		t.Fatalf("ParseDHCPLeases: %v", err)
	}
	if ip != "192.168.64.3" {
		t.Errorf("got %q, want 192.168.64.3", ip)
	}
}

func TestParseDHCPLeases_MultipleVMs(t *testing.T) {
	leases := `{
	name=node1
	ip_address=192.168.64.3
	hw_address=ff,0:0:0:2:0:1:0:1:31:5a:ff:52:52:54:0:0:0:2
	lease=0x69c850e4
}
{
	name=node2
	ip_address=192.168.64.4
	hw_address=ff,0:0:0:2:0:1:0:1:31:5b:0:ba:52:54:0:0:0:3
	lease=0x69c8524b
}
`
	ip1, err := ParseDHCPLeases(leases, "52:54:00:00:00:02")
	if err != nil {
		t.Fatalf("node1: %v", err)
	}
	if ip1 != "192.168.64.3" {
		t.Errorf("node1 = %q, want 192.168.64.3", ip1)
	}

	ip2, err := ParseDHCPLeases(leases, "52:54:00:00:00:03")
	if err != nil {
		t.Fatalf("node2: %v", err)
	}
	if ip2 != "192.168.64.4" {
		t.Errorf("node2 = %q, want 192.168.64.4", ip2)
	}
}

func TestParseDHCPLeases_NotFound(t *testing.T) {
	leases := `{
	ip_address=192.168.64.3
	hw_address=ff,0:0:0:2:0:1:0:1:31:5a:ff:52:52:54:0:0:0:2
	lease=0x69c850e4
}
`
	_, err := ParseDHCPLeases(leases, "52:54:00:00:00:99")
	if err == nil {
		t.Fatal("expected error for MAC not in leases")
	}
}

func TestParseDHCPLeases_Empty(t *testing.T) {
	_, err := ParseDHCPLeases("", "52:54:00:00:00:02")
	if err == nil {
		t.Fatal("expected error for empty leases")
	}
}

func TestNormalizeMACForDHCP(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"52:54:00:00:00:02", "52:54:0:0:0:2"},
		{"AA:BB:CC:DD:EE:FF", "aa:bb:cc:dd:ee:ff"},
		{"01:02:03:04:05:06", "1:2:3:4:5:6"},
		{"10:20:30:40:50:60", "10:20:30:40:50:60"},
	}
	for _, tt := range tests {
		got := normalizeMACForDHCP(tt.input)
		if got != tt.want {
			t.Errorf("normalizeMACForDHCP(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- ARP fallback tests ---

func TestParseARPOutput_MacOS(t *testing.T) {
	output := `? (192.168.64.15) at 52:54:0:a0:33:bd on bridge101 ifscope [bridge]
? (192.168.64.1) at 3e:22:fb:b8:ca:64 on bridge100 ifscope [bridge]
`
	ip, err := ParseARPOutput(output, "52:54:00:a0:33:bd")
	if err != nil {
		t.Fatalf("ParseARPOutput: %v", err)
	}
	if ip != "192.168.64.15" {
		t.Errorf("got %q, want 192.168.64.15", ip)
	}
}

func TestParseARPOutput_NotFound(t *testing.T) {
	output := `? (192.168.64.1) at 3e:22:fb:b8:ca:64 on bridge100 ifscope [bridge]
`
	_, err := ParseARPOutput(output, "52:54:00:99:99:99")
	if err == nil {
		t.Fatal("expected error for MAC not in ARP table")
	}
}

func TestParseDHCPLeases_ReturnsLatestLease(t *testing.T) {
	// Same MAC with multiple leases — should return the first match.
	// In practice bootpd keeps the latest at the top.
	leases := `{
	name=my-vm
	ip_address=192.168.64.5
	hw_address=ff,0:0:0:2:0:1:0:1:31:5c:0:0:52:54:0:0:0:2
	lease=0x69c85300
}
{
	name=my-vm
	ip_address=192.168.64.3
	hw_address=ff,0:0:0:2:0:1:0:1:31:5a:ff:52:52:54:0:0:0:2
	lease=0x69c850e4
}
`
	ip, err := ParseDHCPLeases(leases, "52:54:00:00:00:02")
	if err != nil {
		t.Fatalf("ParseDHCPLeases: %v", err)
	}
	if ip != "192.168.64.5" {
		t.Errorf("got %q, want 192.168.64.5 (latest lease)", ip)
	}
}
