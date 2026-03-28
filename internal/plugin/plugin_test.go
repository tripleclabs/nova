package plugin

import (
	"os"
	"path/filepath"
	"testing"

	lua "github.com/yuin/gopher-lua"
)

func writePlugin(t *testing.T, dir, name, content string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, name), []byte(content), 0644)
}

func TestLoadAll_Empty(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.LoadAll(); err != nil {
		t.Fatal(err)
	}
	if mgr.PluginCount() != 0 {
		t.Errorf("count = %d, want 0", mgr.PluginCount())
	}
}

func TestLoadAll_SinglePlugin(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "hello.lua", `
nova.log("hello plugin loaded")
`)

	mgr, _ := NewManager(dir)
	mgr.LoadAll()

	if mgr.PluginCount() != 1 {
		t.Errorf("count = %d, want 1", mgr.PluginCount())
	}
}

func TestLoadAll_SkipsNonLua(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "readme.md", "not a plugin")
	writePlugin(t, dir, "actual.lua", `nova.log("loaded")`)

	mgr, _ := NewManager(dir)
	mgr.LoadAll()

	if mgr.PluginCount() != 1 {
		t.Errorf("count = %d, want 1", mgr.PluginCount())
	}
}

func TestLoadAll_BadPluginSkipped(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "bad.lua", `this is not valid lua {{{{`)
	writePlugin(t, dir, "good.lua", `nova.log("ok")`)

	mgr, _ := NewManager(dir)
	mgr.LoadAll()

	if mgr.PluginCount() != 1 {
		t.Errorf("count = %d, want 1 (bad plugin should be skipped)", mgr.PluginCount())
	}
}

func TestHook_DNSResolve(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "dns.lua", `
nova.register("dns_resolve", function(hostname)
    if hostname == "myapp.local" then
        return "10.0.0.99"
    end
    return nil
end)
`)

	mgr, _ := NewManager(dir)
	mgr.LoadAll()

	// Matching hostname.
	result := mgr.CallHook(HookDNSResolve, "myapp.local")
	if result != "10.0.0.99" {
		t.Errorf("dns_resolve(myapp.local) = %q, want 10.0.0.99", result)
	}

	// Non-matching hostname.
	result = mgr.CallHook(HookDNSResolve, "other.local")
	if result != "" {
		t.Errorf("dns_resolve(other.local) = %q, want empty", result)
	}
}

func TestHook_Lifecycle(t *testing.T) {
	dir := t.TempDir()
	// Plugin that records events in a global table.
	writePlugin(t, dir, "lifecycle.lua", `
_events = {}

nova.register("on_vm_start", function(name)
    table.insert(_events, "start:" .. name)
end)

nova.register("on_vm_stop", function(name)
    table.insert(_events, "stop:" .. name)
end)
`)

	mgr, _ := NewManager(dir)
	mgr.LoadAll()

	mgr.CallHook(HookOnVMStart, "web-server")
	mgr.CallHook(HookOnVMStop, "web-server")

	// Verify events were recorded (access plugin's Lua state).
	p := mgr.plugins[0]
	events := p.L.GetGlobal("_events")
	tbl, ok := events.(*lua.LTable)
	if !ok {
		t.Fatal("_events should be a table")
	}
	if tbl.Len() != 2 {
		t.Errorf("events len = %d, want 2", tbl.Len())
	}
}

func TestHook_NoRegistration(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "empty.lua", `nova.log("no hooks registered")`)

	mgr, _ := NewManager(dir)
	mgr.LoadAll()

	// Should not panic on unregistered hook.
	result := mgr.CallHook(HookDNSResolve, "test.local")
	if result != "" {
		t.Errorf("unregistered hook should return empty, got %q", result)
	}
}

func TestHook_MultiplePlugins(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "dns1.lua", `
nova.register("dns_resolve", function(hostname)
    if hostname == "a.local" then return "1.1.1.1" end
end)
`)
	writePlugin(t, dir, "dns2.lua", `
nova.register("dns_resolve", function(hostname)
    if hostname == "b.local" then return "2.2.2.2" end
end)
`)

	mgr, _ := NewManager(dir)
	mgr.LoadAll()

	if r := mgr.CallHook(HookDNSResolve, "a.local"); r != "1.1.1.1" {
		t.Errorf("a.local = %q, want 1.1.1.1", r)
	}
	if r := mgr.CallHook(HookDNSResolve, "b.local"); r != "2.2.2.2" {
		t.Errorf("b.local = %q, want 2.2.2.2", r)
	}
}

func TestCallHookAll(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "p1.lua", `
nova.register("on_vm_start", function(name) return "p1:" .. name end)
`)
	writePlugin(t, dir, "p2.lua", `
nova.register("on_vm_start", function(name) return "p2:" .. name end)
`)

	mgr, _ := NewManager(dir)
	mgr.LoadAll()

	results := mgr.CallHookAll(HookOnVMStart, "web")
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
}

func TestRegisterHostFunc(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "custom.lua", `
local val = nova.get_secret("db-password")
_captured = val
`)

	mgr, _ := NewManager(dir)
	mgr.RegisterHostFunc("get_secret", func(L *lua.LState) int {
		key := L.CheckString(1)
		if key == "db-password" {
			L.Push(lua.LString("s3cret"))
		} else {
			L.Push(lua.LNil)
		}
		return 1
	})
	mgr.LoadAll()

	p := mgr.plugins[0]
	captured := p.L.GetGlobal("_captured")
	if captured.String() != "s3cret" {
		t.Errorf("captured = %q, want s3cret", captured.String())
	}
}

func TestClose(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "a.lua", `nova.log("a")`)

	mgr, _ := NewManager(dir)
	mgr.LoadAll()

	if mgr.PluginCount() != 1 {
		t.Fatal("should have 1 plugin")
	}

	mgr.Close()

	if mgr.PluginCount() != 0 {
		t.Error("after Close, count should be 0")
	}
}
