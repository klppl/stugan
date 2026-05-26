// Package plugin is the only package that imports the plugin runtime
// (github.com/yuin/gopher-lua).
//
// core fires hooks through the PluginHost interface and receives back
// decisions (allow/modify/drop); it never sees a Lua type. This package
// implements the Lua host: the scripts/ loader, the fsnotify hot-reload
// watcher, the hook registry, and the stugan.* Lua API bindings. A
// crashing script is isolated (Lua panics recovered, script disabled) and
// never kills the daemon. A WASM host (wazero) can be added later behind
// the same PluginHost interface. See docs/plugins.md.
//
// The host and bindings land in Phase 5.
package plugin
