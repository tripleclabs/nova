// Package plugin implements Nova's Lua-based plugin system.
// Plugins are .lua files discovered from ~/.nova/plugins/ that can hook into
// DNS resolution, VM lifecycle events, and network conditioning.
package plugin

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	lua "github.com/yuin/gopher-lua"
)

// Hook points that plugins can register for.
const (
	HookDNSResolve  = "dns_resolve"   // (hostname) -> ip or nil
	HookOnVMStart   = "on_vm_start"   // (vm_name)
	HookOnVMStop    = "on_vm_stop"    // (vm_name)
	HookOnSnapshot  = "on_snapshot"   // (snapshot_name)
	HookOnLink      = "on_link"       // (node_a, node_b, action)
)

// Plugin represents a loaded Lua plugin.
type Plugin struct {
	Name string
	Path string
	L    *lua.LState
}

// Manager discovers, loads, and dispatches to Lua plugins.
type Manager struct {
	mu        sync.RWMutex
	pluginDir string
	plugins   []*Plugin
	hostFuncs map[string]lua.LGFunction // Extra host functions injected by the app.
}

// NewManager creates a plugin Manager that loads plugins from the given directory.
func NewManager(pluginDir string) (*Manager, error) {
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		return nil, fmt.Errorf("creating plugin dir: %w", err)
	}
	return &Manager{
		pluginDir: pluginDir,
		hostFuncs: make(map[string]lua.LGFunction),
	}, nil
}

// RegisterHostFunc adds a Go function that plugins can call via nova.<name>().
func (m *Manager) RegisterHostFunc(name string, fn lua.LGFunction) {
	m.hostFuncs[name] = fn
}

// LoadAll discovers and loads all .lua files in the plugin directory.
func (m *Manager) LoadAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	entries, err := os.ReadDir(m.pluginDir)
	if err != nil {
		return nil
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".lua") {
			continue
		}
		path := filepath.Join(m.pluginDir, e.Name())
		p, err := m.loadPlugin(path)
		if err != nil {
			slog.Warn("failed to load plugin", "path", path, "error", err)
			continue
		}
		m.plugins = append(m.plugins, p)
		slog.Info("loaded plugin", "name", p.Name, "path", path)
	}

	return nil
}

func (m *Manager) loadPlugin(path string) (*Plugin, error) {
	L := lua.NewState(lua.Options{SkipOpenLibs: false})

	// Expose the "nova" module with host functions.
	novaMod := L.NewTable()
	for name, fn := range m.hostFuncs {
		L.SetField(novaMod, name, L.NewFunction(fn))
	}

	// Built-in host functions.
	L.SetField(novaMod, "log", L.NewFunction(luaLog))
	L.SetField(novaMod, "register", L.NewFunction(luaRegister(L)))

	L.SetGlobal("nova", novaMod)

	if err := L.DoFile(path); err != nil {
		L.Close()
		return nil, fmt.Errorf("executing %s: %w", path, err)
	}

	name := strings.TrimSuffix(filepath.Base(path), ".lua")
	return &Plugin{Name: name, Path: path, L: L}, nil
}

// CallHook dispatches a hook event to all loaded plugins that registered for it.
// Returns the first non-nil string result (useful for dns_resolve), or "".
func (m *Manager) CallHook(hook string, args ...string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, p := range m.plugins {
		result := callPluginHook(p, hook, args...)
		if result != "" {
			return result
		}
	}
	return ""
}

// CallHookAll dispatches a hook to all plugins and collects all non-empty results.
func (m *Manager) CallHookAll(hook string, args ...string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []string
	for _, p := range m.plugins {
		if r := callPluginHook(p, hook, args...); r != "" {
			results = append(results, r)
		}
	}
	return results
}

// Close shuts down all plugin Lua states.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.plugins {
		p.L.Close()
	}
	m.plugins = nil
}

// PluginCount returns the number of loaded plugins.
func (m *Manager) PluginCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.plugins)
}

func callPluginHook(p *Plugin, hook string, args ...string) string {
	fn := p.L.GetGlobal("_nova_hooks")
	if fn.Type() != lua.LTTable {
		return ""
	}
	hookTbl := fn.(*lua.LTable)
	handler := hookTbl.RawGetString(hook)
	if handler.Type() != lua.LTFunction {
		return ""
	}

	luaArgs := make([]lua.LValue, len(args))
	for i, a := range args {
		luaArgs[i] = lua.LString(a)
	}

	if err := p.L.CallByParam(lua.P{
		Fn:      handler,
		NRet:    1,
		Protect: true,
	}, luaArgs...); err != nil {
		slog.Warn("plugin hook error", "plugin", p.Name, "hook", hook, "error", err)
		return ""
	}

	ret := p.L.Get(-1)
	p.L.Pop(1)

	if str, ok := ret.(lua.LString); ok {
		return string(str)
	}
	return ""
}

// --- Built-in Lua host functions ---

// nova.log(message) — logs a message from a plugin.
func luaLog(L *lua.LState) int {
	msg := L.CheckString(1)
	slog.Info("plugin", "msg", msg)
	return 0
}

// nova.register(hook_name, function) — registers a hook handler.
// Stored in the global _nova_hooks table.
func luaRegister(L *lua.LState) lua.LGFunction {
	return func(L *lua.LState) int {
		hook := L.CheckString(1)
		fn := L.CheckFunction(2)

		hooks := L.GetGlobal("_nova_hooks")
		if hooks.Type() != lua.LTTable {
			tbl := L.NewTable()
			L.SetGlobal("_nova_hooks", tbl)
			hooks = tbl
		}
		L.SetField(hooks.(*lua.LTable), hook, fn)
		return 0
	}
}
