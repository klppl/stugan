// Package plugin is the only package that imports the Lua runtime
// (github.com/yuin/gopher-lua). It implements core.PluginHost: a Lua host
// that loads scripts from a directory, hot-reloads them, and runs their
// hooks. core fires hooks through the PluginHost interface and never sees a
// Lua value; scripts act on IRC state through the core.API handed in here.
//
// All Lua execution happens on a single goroutine fed by a work queue, so
// the per-script LStates need no locking and hooks never race. A crashing
// or erroring script is isolated (Lua errors recovered, the script disabled
// after repeated failures) and never brings down the daemon.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	lua "github.com/yuin/gopher-lua"

	"github.com/klippelism/stugan/internal/core"
)

// defaultHookTimeout bounds a single hook invocation so a runaway script
// cannot wedge the plugin goroutine.
const defaultHookTimeout = 3 * time.Second

// maxScriptErrors is how many runtime errors a script may raise before it is
// disabled until its next reload.
const maxScriptErrors = 10

var _ core.PluginHost = (*Host)(nil)

// KV is the persistence seam for stugan.kv. The plugin host caches values
// in memory for fast access; if a KV is provided it loads on first touch
// and writes through on every set/delete so values survive daemon restart.
// Implemented by *store.Store; nil means "in-memory only" (the historical
// behaviour, still used by tests).
type KV interface {
	GetAll(script string) map[string]string
	Set(script, key, value string) error
	Delete(script, key string) error
}

// HTTPDoer performs an HTTP request. *http.Client satisfies it. The host takes
// an injected doer rather than building its own so the SSRF guard lives in one
// place (internal/safehttp) and tests can supply a permissive client. A nil
// doer disables the stugan.http binding.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Options configures the Lua host.
type Options struct {
	API      core.API
	Logger   *slog.Logger
	Dir      string                    // scripts directory
	Settings map[string]map[string]any // per-script config, by name
	Sandbox  bool                      // restrict the Lua stdlib
	Timeout  time.Duration             // per-hook timeout (0 = default)
	KV       KV                        // optional persistence for stugan.kv
	HTTP     HTTPDoer                  // optional client for stugan.http (nil disables it)
}

// Host is a Lua implementation of core.PluginHost.
type Host struct {
	api      core.API
	log      *slog.Logger
	dir      string
	settings map[string]map[string]any
	sandbox  bool
	timeout  time.Duration
	kvStore  KV

	httpClient HTTPDoer
	httpSem    chan struct{} // bounds concurrent stugan.http requests

	// ctx is cancelled by Close so in-flight HTTP requests abort promptly
	// rather than holding shutdown for their full timeout.
	ctx    context.Context
	cancel context.CancelFunc

	jobs     chan func()
	quit     chan struct{}
	quitOnce sync.Once
	wg       sync.WaitGroup
	watcher  *fsnotify.Watcher

	// The fields below are touched only on the plugin goroutine (during New
	// before the goroutine starts, and via do() afterwards), so they need no
	// locking.
	scripts         map[string]*script
	kv              map[string]map[string]string // per-script KV, survives reload
	msgHooks        []*hook
	inputHooks      []*hook
	topicHooks      []*hook
	completionHooks []*hook
	signalHooks     map[string][]*hook
	cmdHooks        map[string]*hook
	timers          []*timerHook
	nextID          int
	unhookers       map[int]unhooker
}

// unhooker undoes one hook/timer registration. The owning script is recorded
// so unloadScript can drop a script's entries without invoking them.
type unhooker struct {
	script *script
	fn     func()
}

type script struct {
	name     string
	path     string         // source file, for reload
	desc     string         // set by stugan.describe()
	settings []*settingDecl // declared via stugan.setting(), in declaration order
	L        *lua.LState
	errs     int
	disabled bool
}

// settingDecl is one stugan.setting() declaration: metadata for the management
// UI plus an optional apply callback the host runs when the value changes.
type settingDecl struct {
	name    string
	typ     string // "text" | "number" | "select"
	label   string
	help    string
	def     string
	secret  bool
	options []string
	apply   *lua.LFunction // optional; called with the (string) value on change
}

type hook struct {
	script *script
	fn     *lua.LFunction
	prio   int
	id     int
}

type timerHook struct {
	script *script
	fn     *lua.LFunction
	id     int
	ticker *time.Ticker
	stop   chan struct{}
}

// New builds and starts the host: it launches the work-queue goroutine,
// loads every *.lua in Dir, and (if Dir exists) watches it for changes.
func New(opts Options) (*Host, error) {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultHookTimeout
	}
	h := &Host{
		api:         opts.API,
		log:         log,
		dir:         opts.Dir,
		settings:    opts.Settings,
		sandbox:     opts.Sandbox,
		timeout:     timeout,
		kvStore:     opts.KV,
		httpClient:  opts.HTTP,
		jobs:        make(chan func()),
		quit:        make(chan struct{}),
		scripts:     map[string]*script{},
		kv:          map[string]map[string]string{},
		signalHooks: map[string][]*hook{},
		cmdHooks:    map[string]*hook{},
		unhookers:   map[int]unhooker{},
	}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	if h.httpClient != nil {
		h.httpSem = make(chan struct{}, maxConcurrentHTTP)
	}

	h.wg.Go(h.loop)

	h.do(h.loadAll)

	if err := h.startWatcher(); err != nil {
		h.log.Warn("plugin hot-reload disabled", "err", err)
	}
	return h, nil
}

// loop is the single goroutine on which all Lua runs.
func (h *Host) loop() {
	for {
		select {
		case <-h.quit:
			return
		case fn := <-h.jobs:
			fn()
		}
	}
}

// do runs fn on the plugin goroutine and waits for it. After Close it is a
// no-op, so callers never block on a stopped host.
func (h *Host) do(fn func()) {
	done := make(chan struct{})
	select {
	case h.jobs <- func() { defer close(done); fn() }:
		<-done
	case <-h.quit:
	}
}

// Dispatch implements core.PluginHost. It runs the hooks for ev on the
// plugin goroutine and returns the (possibly rewritten) event with
// keep=false if a hook dropped it (messages) or claimed it (commands).
func (h *Host) Dispatch(_ context.Context, ev core.Event) (core.Event, bool) {
	out, keep := ev, true
	h.do(func() { out, keep = h.dispatch(ev) })
	return out, keep
}

// Commands implements core.PluginHost.
func (h *Host) Commands() []string {
	var cmds []string
	h.do(func() {
		for name := range h.cmdHooks {
			cmds = append(cmds, name)
		}
	})
	return cmds
}

// Complete implements core.PluginHost. It runs the registered hook_completion
// callbacks on the plugin goroutine and gathers their candidates.
func (h *Host) Complete(word, network, buffer string) []string {
	var out []string
	h.do(func() { out = h.runCompletionHooks(word, network, buffer) })
	return out
}

// Plugins implements core.PluginHost. It merges the loaded scripts with the
// *.lua files in the scripts dir that are not currently loaded, so the
// management UI can show both running plugins and ones available to load.
func (h *Host) Plugins() []core.PluginInfo {
	var out []core.PluginInfo
	h.do(func() {
		seen := map[string]bool{}
		for name, s := range h.scripts {
			seen[name] = true
			out = append(out, core.PluginInfo{
				Name:        name,
				Description: s.desc,
				Loaded:      true,
				Disabled:    s.disabled,
				Errors:      s.errs,
				Commands:    h.commandsFor(s),
				Hooks:       h.hookCount(s),
				Settings:    h.settingsFor(s),
			})
		}
		// Unloaded files on disk: present but not running.
		if h.dir != "" {
			entries, _ := os.ReadDir(h.dir)
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".lua") {
					continue
				}
				if name := scriptName(e.Name()); !seen[name] {
					seen[name] = true
					out = append(out, core.PluginInfo{Name: name})
				}
			}
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// commandsFor returns the /command names registered by script s (plugin
// goroutine).
func (h *Host) commandsFor(s *script) []string {
	var cmds []string
	for name, hk := range h.cmdHooks {
		if hk.script == s {
			cmds = append(cmds, name)
		}
	}
	sort.Strings(cmds)
	return cmds
}

// hookCount totals the message/input/signal/timer hooks owned by script s
// (plugin goroutine). Command hooks are reported separately via Commands.
func (h *Host) hookCount(s *script) int {
	n := 0
	for _, hk := range h.msgHooks {
		if hk.script == s {
			n++
		}
	}
	for _, hk := range h.inputHooks {
		if hk.script == s {
			n++
		}
	}
	for _, hk := range h.topicHooks {
		if hk.script == s {
			n++
		}
	}
	for _, hk := range h.completionHooks {
		if hk.script == s {
			n++
		}
	}
	for _, hks := range h.signalHooks {
		for _, hk := range hks {
			if hk.script == s {
				n++
			}
		}
	}
	for _, t := range h.timers {
		if t.script == s {
			n++
		}
	}
	return n
}

// kvCache returns script s's in-memory kv map, lazy-filling it from the
// backing store on first touch (plugin goroutine). Shared by the stugan.kv
// bindings and the settings machinery so they see one coherent cache.
func (h *Host) kvCache(s *script) map[string]string {
	if h.kv[s.name] == nil {
		if h.kvStore != nil {
			h.kv[s.name] = h.kvStore.GetAll(s.name)
		} else {
			h.kv[s.name] = map[string]string{}
		}
	}
	return h.kv[s.name]
}

// kvGet/kvSet are the Go-side equivalents of stugan.kv.get/set, writing
// through to the backing store and keeping the cache coherent.
func (h *Host) kvGet(s *script, key string) (string, bool) {
	v, ok := h.kvCache(s)[key]
	return v, ok
}

func (h *Host) kvSet(s *script, key, val string) {
	h.kvCache(s)[key] = val
	if h.kvStore != nil {
		if err := h.kvStore.Set(s.name, key, val); err != nil {
			h.log.Warn("plugin kv set persist", "script", s.name, "err", err)
		}
	}
}

// settingValue is a setting's current effective value: the kv override if set,
// otherwise the declared default.
func (h *Host) settingValue(s *script, d *settingDecl) string {
	if v, ok := h.kvGet(s, d.name); ok {
		return v
	}
	return d.def
}

// settingsFor projects a script's declared settings for the management UI.
// Secret values are withheld (Value left blank). Plugin goroutine.
func (h *Host) settingsFor(s *script) []core.PluginSetting {
	if len(s.settings) == 0 {
		return nil
	}
	out := make([]core.PluginSetting, 0, len(s.settings))
	for _, d := range s.settings {
		ps := core.PluginSetting{
			Name: d.name, Type: d.typ, Label: d.label, Help: d.help,
			Default: d.def, Secret: d.secret, Options: d.options,
		}
		if !d.secret {
			ps.Value = h.settingValue(s, d)
		}
		out = append(out, ps)
	}
	return out
}

// SetPluginSetting implements core.PluginHost. It validates value against the
// named setting's type, persists it to the script's kv, and runs the setting's
// apply callback — all on the plugin goroutine so the cache stays coherent.
func (h *Host) SetPluginSetting(script, key, value string) error {
	var rerr error
	h.do(func() {
		s := h.scripts[script]
		if s == nil {
			rerr = fmt.Errorf("plugin %q is not loaded", script)
			return
		}
		var d *settingDecl
		for _, x := range s.settings {
			if x.name == key {
				d = x
				break
			}
		}
		if d == nil {
			rerr = fmt.Errorf("plugin %q has no setting %q", script, key)
			return
		}
		v, err := coerceSetting(d, value)
		if err != nil {
			rerr = err
			return
		}
		h.kvSet(s, key, v)
		if d.apply != nil {
			h.call(s, d.apply, func(L *lua.LState) { L.Push(lua.LString(v)) }, 0)
		}
	})
	return rerr
}

// coerceSetting validates and normalizes a raw value against a setting's type.
func coerceSetting(d *settingDecl, value string) (string, error) {
	switch d.typ {
	case "number":
		if _, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err != nil {
			return "", fmt.Errorf("setting %q expects a number, got %q", d.name, value)
		}
		return strings.TrimSpace(value), nil
	case "select":
		for _, opt := range d.options {
			if value == opt {
				return value, nil
			}
		}
		return "", fmt.Errorf("setting %q must be one of %v, got %q", d.name, d.options, value)
	default: // "text" and anything unknown
		return value, nil
	}
}

// LoadPlugin loads (or reloads) the named script from the scripts dir.
func (h *Host) LoadPlugin(name string) error {
	path, err := h.scriptPath(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("no such plugin %q", name)
	}
	h.do(func() { h.loadScript(path) })
	if !h.isLoaded(name) {
		return fmt.Errorf("plugin %q failed to load (check the log)", name)
	}
	return nil
}

// UnloadPlugin tears down a loaded script's hooks and LState.
func (h *Host) UnloadPlugin(name string) error {
	if _, err := h.scriptPath(name); err != nil {
		return err
	}
	if !h.isLoaded(name) {
		return fmt.Errorf("plugin %q is not loaded", name)
	}
	h.do(func() { h.unloadScript(name) })
	return nil
}

// ReloadPlugin re-reads a script from disk, dropping its old hooks first.
func (h *Host) ReloadPlugin(name string) error { return h.LoadPlugin(name) }

// scriptPath resolves a bare script name to its file, rejecting anything
// that looks like a path (traversal guard — name comes from the client).
func (h *Host) scriptPath(name string) (string, error) {
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("invalid plugin name %q", name)
	}
	if h.dir == "" {
		return "", errors.New("no scripts directory")
	}
	return filepath.Join(h.dir, name+".lua"), nil
}

func (h *Host) isLoaded(name string) bool {
	loaded := false
	h.do(func() { _, loaded = h.scripts[name] })
	return loaded
}

// Close implements core.PluginHost: stop watching, stop timers, drain the
// work goroutine, and close every LState.
func (h *Host) Close() error {
	if h.watcher != nil {
		h.watcher.Close()
	}
	h.cancel() // abort in-flight HTTP so shutdown doesn't wait out their timeout
	h.quitOnce.Do(func() { close(h.quit) })
	h.wg.Wait()
	// The loop goroutine has exited; teardown is now race-free.
	for _, t := range h.timers {
		t.ticker.Stop()
		close(t.stop)
	}
	for _, s := range h.scripts {
		s.L.Close()
	}
	return nil
}

// dispatch routes one event to the appropriate hooks (plugin goroutine).
func (h *Host) dispatch(ev core.Event) (core.Event, bool) {
	switch ev.Type {
	case core.EvMessageIn:
		return h.runMessageHooks(ev)
	case core.EvMessageOut:
		return h.runInputHooks(ev)
	case core.EvCommand:
		return h.runCommand(ev)
	case core.EvTopic:
		// Topic hooks may rewrite the topic text; signal hooks are then
		// notified with the (possibly rewritten) topic.
		out, keep := h.runTopicHooks(ev)
		if keep {
			if name, ok := signalName(out.Type); ok {
				h.runSignalHooks(name, out)
			}
		}
		return out, keep
	default:
		if name, ok := signalName(ev.Type); ok {
			h.runSignalHooks(name, ev)
		}
		return ev, true
	}
}

// runMessageHooks runs hook_message in priority order; a hook may rewrite
// the message (return a table) or drop it (return nil/false).
func (h *Host) runMessageHooks(ev core.Event) (core.Event, bool) {
	if ev.Message == nil {
		return ev, true
	}
	msg := *ev.Message
	for _, hk := range h.msgHooks {
		if hk.script.disabled {
			continue
		}
		ret, ok := h.call(hk.script, hk.fn, func(L *lua.LState) { L.Push(msgToTable(L, &msg)) }, 1)
		if !ok {
			continue // errored hook is skipped, message passes through
		}
		switch v := ret.(type) {
		case *lua.LNilType:
			return ev, false // dropped
		case lua.LBool:
			if !bool(v) {
				return ev, false
			}
		case *lua.LTable:
			msg = tableToMsg(v, msg)
		}
	}
	out := ev
	out.Message = &msg
	return out, true
}

// runInputHooks runs hook_input over the outgoing text; a hook returns a
// replacement string or nil to drop the line.
func (h *Host) runInputHooks(ev core.Event) (core.Event, bool) {
	if ev.Message == nil {
		return ev, true
	}
	msg := *ev.Message
	nick := h.api.Nick(msg.Network)
	for _, hk := range h.inputHooks {
		if hk.script.disabled {
			continue
		}
		text := msg.Text
		ret, ok := h.call(hk.script, hk.fn, func(L *lua.LState) {
			L.Push(lua.LString(text))
			L.Push(ctxTable(L, msg.Network, msg.Buffer, nick))
		}, 1)
		if !ok {
			continue
		}
		switch v := ret.(type) {
		case *lua.LNilType:
			return ev, false
		case lua.LBool:
			if !bool(v) {
				return ev, false
			}
		case lua.LString:
			msg.Text = string(v)
		}
	}
	out := ev
	out.Message = &msg
	return out, true
}

// runTopicHooks runs hook_topic in priority order over an EvTopic event. A
// hook returns a string (or a table with a `text` field) to rewrite the topic,
// or nil/nothing to leave it unchanged. Topics aren't droppable — they're
// channel state, not a stream of lines — so the keep flag is always true.
func (h *Host) runTopicHooks(ev core.Event) (core.Event, bool) {
	text := ev.Text
	for _, hk := range h.topicHooks {
		if hk.script.disabled {
			continue
		}
		cur := text
		ret, ok := h.call(hk.script, hk.fn, func(L *lua.LState) {
			L.Push(topicTable(L, ev, cur))
		}, 1)
		if !ok {
			continue // errored hook is skipped, topic passes through unchanged
		}
		switch v := ret.(type) {
		case lua.LString:
			text = string(v)
		case *lua.LTable:
			if s, ok := v.RawGetString("text").(lua.LString); ok {
				text = string(s)
			}
		}
	}
	out := ev
	out.Text = text
	return out, true
}

// runCommand dispatches a /command to its registered hook, if any.
func (h *Host) runCommand(ev core.Event) (core.Event, bool) {
	hk := h.cmdHooks[strings.ToLower(ev.Command)]
	if hk == nil || hk.script.disabled {
		return ev, true // not ours; let the engine handle it
	}
	args := ev.Args
	nick := h.api.Nick(ev.Network)
	h.call(hk.script, hk.fn, func(L *lua.LState) {
		L.Push(stringArray(L, args))
		L.Push(ctxTable(L, ev.Network, ev.Buffer, nick))
	}, 0)
	return ev, false // consumed
}

// runCompletionHooks gathers candidates from hook_completion callbacks, in
// priority order. Each hook is handed the partial word and the buffer ctx and
// returns an array of replacement strings (or nil); the results are flattened.
func (h *Host) runCompletionHooks(word, network, buffer string) []string {
	if len(h.completionHooks) == 0 {
		return nil
	}
	nick := h.api.Nick(network)
	var out []string
	for _, hk := range h.completionHooks {
		if hk.script.disabled {
			continue
		}
		ret, ok := h.call(hk.script, hk.fn, func(L *lua.LState) {
			L.Push(lua.LString(word))
			L.Push(ctxTable(L, network, buffer, nick))
		}, 1)
		if !ok {
			continue
		}
		tbl, ok := ret.(*lua.LTable)
		if !ok {
			continue
		}
		tbl.ForEach(func(_, v lua.LValue) {
			if s, ok := v.(lua.LString); ok && string(s) != "" {
				out = append(out, string(s))
			}
		})
	}
	return out
}

// runSignalHooks notifies hook_signal subscribers for an event.
func (h *Host) runSignalHooks(name string, ev core.Event) {
	for _, hk := range h.signalHooks[name] {
		if hk.script.disabled {
			continue
		}
		h.call(hk.script, hk.fn, func(L *lua.LState) { L.Push(signalTable(L, ev)) }, 0)
	}
}

// runTimer fires a timer's callback (plugin goroutine).
func (h *Host) runTimer(t *timerHook) {
	// The script may have been unloaded or reloaded (LState closed) between
	// the tick and this job running; calling into a closed LState panics
	// outside PCall's recover and would take the daemon down.
	if h.scripts[t.script.name] != t.script || t.script.disabled {
		return
	}
	h.call(t.script, t.fn, func(*lua.LState) {}, 0)
}

// call invokes fn (belonging to script s) with a timeout and recovers Lua
// errors. push pushes the arguments; nret is the number of expected return
// values (0 or 1). It returns the single return value when nret==1.
func (h *Host) call(s *script, fn *lua.LFunction, push func(L *lua.LState), nret int) (lua.LValue, bool) {
	L := s.L
	ctx, cancel := context.WithTimeout(context.Background(), h.timeout)
	defer cancel()
	L.SetContext(ctx)

	before := L.GetTop()
	L.Push(fn)
	push(L)
	nargs := L.GetTop() - before - 1

	if err := L.PCall(nargs, nret, nil); err != nil {
		h.scriptError(s, err)
		return nil, false
	}
	if nret == 0 {
		return nil, true
	}
	ret := L.Get(-1)
	L.Pop(1)
	return ret, true
}

// scriptError logs a runtime error and disables the script once it has
// failed too many times.
func (h *Host) scriptError(s *script, err error) {
	s.errs++
	h.log.Error("plugin runtime error", "script", s.name, "err", err, "count", s.errs)
	if s.errs >= maxScriptErrors && !s.disabled {
		s.disabled = true
		h.log.Warn("plugin disabled after repeated errors; fix and save to reload", "script", s.name)
	}
}

// startWatcher sets up fsnotify hot-reload on the scripts directory.
func (h *Host) startWatcher() error {
	if h.dir == "" {
		return errors.New("no scripts dir")
	}
	if err := os.MkdirAll(h.dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(h.dir); err != nil {
		return err
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := w.Add(h.dir); err != nil {
		w.Close()
		return err
	}
	h.watcher = w
	h.wg.Go(h.watch)
	return nil
}

// loadAll loads every *.lua in the scripts dir (plugin goroutine).
func (h *Host) loadAll() {
	if h.dir == "" {
		return
	}
	entries, err := os.ReadDir(h.dir)
	if err != nil {
		if !os.IsNotExist(err) {
			h.log.Warn("read scripts dir", "dir", h.dir, "err", err)
		}
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".lua") {
			continue
		}
		h.loadScript(filepath.Join(h.dir, e.Name()))
	}
}

// scriptName derives a script's identity from its filename.
func scriptName(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".lua")
}
