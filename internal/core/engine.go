// Package core is the GUI- and transport-independent brain of stugan: the
// domain types, the connection state machine, and the event bus through
// which every meaningful event flows. Plugin hooks fire on the bus and may
// drop or mutate mutable events before they are committed.
package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Sink observes committed state changes (post-hook, post-state-update),
// the read side of the bus. Print receives each new buffer line;
// NetworkChanged receives a snapshot of a network whose structure changed
// (connection state, nick, buffers, members, topic). Implementations are
// called synchronously from the engine loop and must not block: marshal or
// enqueue and return.
type Sink interface {
	Print(m Message)
	NetworkChanged(n *Network)
	// NetworkRemoved signals that a network was removed at runtime so
	// observers can drop it.
	NetworkRemoved(networkID string)
	// NetworksReordered signals that the user manually reordered their
	// networks; networkIDs is the full network list in the new display order.
	NetworksReordered(networkIDs []string)
	// ChannelList delivers the result of a channel-browser LIST request.
	ChannelList(network string, items []ChannelListItem)
	// Typing delivers an inbound typing notification (state is
	// active/paused/done).
	Typing(network, buffer, nick, state string)
	// React delivers an inbound emoji reaction: nick reacted to the message
	// target (a msgid) in buffer with reaction. Ephemeral, like Typing.
	React(network, buffer, target, nick, reaction string)
	// Redact delivers an inbound message redaction: the message target (a
	// msgid) in buffer was removed by nick (with an optional reason).
	Redact(network, buffer, target, nick, reason string)
}

// ChannelListItem is one entry in a LIST (channel-browser) result.
type ChannelListItem struct {
	Name  string
	Users int
	Topic string
}

// UnreadCount is the persisted, per-buffer tally of messages newer than the
// user's read marker, computed by the history store at connect time so unread
// badges survive a page reload (the live counter in core.Channel is only ever
// incremented in the browser). See store.UnreadCounts / store.MarkRead.
type UnreadCount struct {
	Network   string
	Buffer    string
	Unread    int
	Highlight int
}

// maxListItems caps a LIST result so a huge network can't exhaust memory or
// produce an enormous frame.
const maxListItems = 2000

// HistoryReader reads recent stored messages for a buffer.
type HistoryReader interface {
	Backlog(ctx context.Context, network, buffer string, beforeSeq int64, limit int) ([]Message, bool, error)
}

// Options configures a new Engine.
type Options struct {
	Logger    *slog.Logger
	Host      PluginHost        // nil → events pass through unchanged
	Sink      Sink              // nil → a logger-backed sink
	User      *User             // nil → a single implicit user
	Highlight *Highlighter      // nil → only nick mentions highlight (via a default)
	Aliases   map[string]string // command aliases with $1/$2/$* substitution
	Connector Connector         // builds connections for runtime AddNetwork
	Networks  NetworkStore      // persists GUI-added networks (optional)
	History   HistoryReader     // reads stored backlog (optional)
}

// Engine owns the domain state and serializes all mutation onto a single
// loop goroutine fed by the event bus. It implements ConnHandler.
//
// State (the user/networks/channels tree) is mutated only by the loop, but
// it is also read concurrently by server goroutines (snapshots), so it is
// guarded by mu: the loop write-locks for the brief mutation, readers
// read-lock. I/O (sink fan-out, conn sends) happens outside the lock.
type Engine struct {
	log     *slog.Logger
	host    PluginHost
	sinks   []Sink
	history HistoryReader

	mu   sync.RWMutex
	user *User

	highlight *Highlighter
	aliases   map[string]string
	connector Connector
	netStore  NetworkStore

	// conns and the run-state below are guarded by mu.
	conns       map[string]IRCConn
	connCancels map[string]context.CancelFunc
	// startupSeq identifies the current post-registration sequence for each
	// network. A reconnect supersedes any older Perform goroutine before it can
	// send another command or auto-join on the new connection.
	startupSeq map[string]uint64
	// joinGates are per-network auto-join gates plugins hold via HoldJoins:
	// while a gate exists the startup auto-join is parked on it instead of
	// sent, until ReleaseJoins — or the gate's fallback timer — flushes it.
	// Guarded by mu; reset on disconnect.
	joinGates map[string]*joinGate
	listAccum map[string][]ChannelListItem // in-progress LIST results
	// pendingWhois records which buffer issued a WHOIS/WHOWAS/WHO/NAMES so we
	// can route the server's numeric replies back to it. Key is
	// "<network>\t<lowercase-target>"; cleared on the matching end-of
	// marker (318/369/315/366). Mutated only on the engine loop goroutine.
	pendingWhois map[string]string
	// pendingKeys records the join key (+k password) supplied with a /join
	// command, so it can be committed to the persisted auto-join list when the
	// matching self-JOIN is confirmed by the server. Key is
	// "<network>\t<lowercase-channel>"; only non-empty keys are stored.
	// Touched only on the engine loop goroutine (like pendingWhois).
	pendingKeys map[string]string

	// pendingState holds per-buffer State a plugin set (via SetBufferState),
	// keyed by network id → lowercased buffer name. It is applied when the
	// buffer is (re)created, so plugin state — e.g. fish.lua's "encrypted"
	// lock flag — survives the buffer not existing yet (set at script load,
	// before JOIN) and a daemon restart (live state is in-memory only).
	// Guarded by mu.
	pendingState map[string]map[string]map[string]string

	// idBase + idSeq mint stable ids for lines the IRC server delivered
	// without a msgid tag (see synthID). idBase is a per-run random prefix;
	// idSeq is bumped atomically because broadcast runs on several goroutines
	// (engine loop, send path, and the plugin goroutine via inject).
	idBase string
	idSeq  atomic.Uint64

	running bool
	runCtx  context.Context
	runWG   sync.WaitGroup

	events chan Event
	done   chan struct{}
	closeo sync.Once
}

// New builds an Engine from opts.
func New(opts Options) *Engine {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	host := opts.Host
	if host == nil {
		host = nopHost{}
	}
	var sinks []Sink
	if opts.Sink != nil {
		sinks = append(sinks, opts.Sink)
	} else {
		sinks = append(sinks, logSink{log})
	}
	user := opts.User
	if user == nil {
		user = &User{ID: "default", Name: "default"}
	}
	hl := opts.Highlight
	if hl == nil {
		hl, _ = NewHighlighter(nil, nil) // nick mentions only
	}
	return &Engine{
		log:          log,
		host:         host,
		sinks:        sinks,
		history:      opts.History,
		user:         user,
		highlight:    hl,
		aliases:      opts.Aliases,
		connector:    opts.Connector,
		netStore:     opts.Networks,
		conns:        map[string]IRCConn{},
		connCancels:  map[string]context.CancelFunc{},
		startupSeq:   map[string]uint64{},
		joinGates:    map[string]*joinGate{},
		listAccum:    map[string][]ChannelListItem{},
		pendingWhois: map[string]string{},
		pendingState: map[string]map[string]map[string]string{},
		pendingKeys:  map[string]string{},
		idBase:       randHex(6),
		events:       make(chan Event, 256),
		done:         make(chan struct{}),
	}
}

// randHex returns n random bytes hex-encoded, used to seed a per-run id base.
// crypto/rand should never fail; if it does, the fixed fallback still yields
// process-unique ids (the seq counter does the real work) — it only weakens
// cross-restart uniqueness.
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "fixed"
	}
	return hex.EncodeToString(b)
}

// AddSink registers an additional committed-line sink (e.g. the WebSocket
// server bridge or the store). Call before Run; not safe to call once the
// loop is running.
func (e *Engine) AddSink(s Sink) { e.sinks = append(e.sinks, s) }

// SetHost installs the plugin host. Call before Run. The host is
// constructed with the engine's API (see API), so the wiring is:
// New → SetHost(plugin.New(eng.API(), ...)). A nil host restores the
// pass-through default.
func (e *Engine) SetHost(h PluginHost) {
	if h == nil {
		h = nopHost{}
	}
	e.host = h
}

// SetHighlighter replaces the highlight ruleset at runtime (from the settings
// UI). A nil highlighter restores nick-mentions-only matching. Safe to call
// from a server goroutine: it takes the same lock the engine loop holds while
// reading e.highlight in applyLocked.
func (e *Engine) SetHighlighter(h *Highlighter) {
	if h == nil {
		h, _ = NewHighlighter(nil, nil)
	}
	e.mu.Lock()
	e.highlight = h
	e.mu.Unlock()
}

// HighlightRules returns the current highlight patterns and exceptions, for
// seeding the settings form in the init snapshot.
func (e *Engine) HighlightRules() (patterns, exceptions []string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.highlight.Patterns(), e.highlight.Exceptions()
}

// SetAliases replaces the command-alias table at runtime (from the settings
// UI). Keys are matched case-insensitively at expansion time, so the caller
// should pass lowercase names. Safe to call from a server goroutine: it takes
// the same lock sendInput holds while reading the table. A nil map clears all
// aliases. The map is cloned so the caller may not retain or mutate it.
func (e *Engine) SetAliases(m map[string]string) {
	e.mu.Lock()
	e.aliases = maps.Clone(m)
	e.mu.Unlock()
}

// Aliases returns a copy of the current command-alias table, for seeding the
// settings form in the init snapshot.
func (e *Engine) Aliases() map[string]string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return maps.Clone(e.aliases)
}

// Plugins lists the plugin scripts the host knows about, for the management
// UI. Safe to call concurrently; the host is fixed after startup.
func (e *Engine) Plugins() []PluginInfo { return e.host.Plugins() }

// Complete returns plugin-contributed tab-completion candidates for the
// partial word being typed in (network, buffer). Read-only; the host runs
// the completion hooks on its own goroutine, so this is safe to call from
// server goroutines while the engine loop runs.
func (e *Engine) Complete(network, buffer, word string) []string {
	return e.host.Complete(word, network, buffer)
}

// LoadPlugin, UnloadPlugin, and ReloadPlugin manage a single script at
// runtime by name (no path separators). They delegate to the plugin host,
// which runs them on its own goroutine; IRC connections are untouched.
func (e *Engine) LoadPlugin(name string) error   { return e.host.LoadPlugin(name) }
func (e *Engine) UnloadPlugin(name string) error { return e.host.UnloadPlugin(name) }
func (e *Engine) ReloadPlugin(name string) error { return e.host.ReloadPlugin(name) }
func (e *Engine) SetPluginSetting(script, key, value string) error {
	return e.host.SetPluginSetting(script, key, value)
}

// AddNetwork registers a pre-built connection and its initial state. Call
// before Run (used for networks loaded at startup).
func (e *Engine) AddNetwork(p NetworkParams, conn IRCConn) {
	e.mu.Lock()
	e.user.Networks = append(e.user.Networks, &Network{
		ID: p.ID, Name: p.Name, Nick: p.Nick, State: StateDisconnected, Params: p.clone(),
	})
	e.conns[p.ID] = conn
	e.mu.Unlock()
}

// AddNetwork-related runtime errors.
var (
	errNetworkExists = errors.New("network already exists")
	errNoConnector   = errors.New("runtime networks not supported")
)

// AddNetworkLive adds a network at runtime: it dials via the Connector,
// registers it, persists it (if a NetworkStore is set), starts its
// connection goroutine when the engine is running, and notifies observers.
// Safe to call from any goroutine (e.g. a server handler).
func (e *Engine) AddNetworkLive(p NetworkParams) error {
	if p.ID == "" || p.Addr == "" {
		return errors.New("network requires id and addr")
	}
	e.mu.Lock()
	if e.connector == nil {
		e.mu.Unlock()
		return errNoConnector
	}
	if e.user.Network(p.ID) != nil {
		e.mu.Unlock()
		return errNetworkExists
	}
	conn, err := e.connector.Dial(p, e)
	if err != nil {
		e.mu.Unlock()
		return err
	}
	e.user.Networks = append(e.user.Networks, &Network{
		ID: p.ID, Name: p.Name, Nick: p.Nick, State: StateDisconnected, Params: p.clone(),
	})
	e.conns[p.ID] = conn
	if e.running {
		e.startConnLocked(p.ID, conn)
	}
	e.mu.Unlock()

	if e.netStore != nil {
		if err := e.netStore.SaveNetwork(p); err != nil {
			e.log.Warn("persist network", "network", p.ID, "err", err)
		}
	}
	e.notifyNetwork(p.ID)
	return nil
}

// RemoveNetwork disconnects and removes a network at runtime, deletes it
// from persistence, and notifies observers.
func (e *Engine) RemoveNetwork(id string) error {
	e.mu.Lock()
	if e.user.Network(id) == nil {
		e.mu.Unlock()
		return errors.New("unknown network")
	}
	if cancel := e.connCancels[id]; cancel != nil {
		cancel()
		delete(e.connCancels, id)
	}
	conn := e.conns[id]
	delete(e.conns, id)
	// Drop per-network bookkeeping so it doesn't accumulate across add/remove
	// churn on a long-lived daemon. The network (and its persistence) is gone;
	// a plugin re-publishes buffer state on rejoin if it's ever re-added.
	delete(e.pendingState, id)
	delete(e.listAccum, id)
	delete(e.startupSeq, id)
	if g := e.joinGates[id]; g != nil {
		g.timer.Stop()
		delete(e.joinGates, id)
	}
	for i, n := range e.user.Networks {
		if n.ID == id {
			e.user.Networks = append(e.user.Networks[:i], e.user.Networks[i+1:]...)
			break
		}
	}
	e.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
	if e.netStore != nil {
		if err := e.netStore.DeleteNetwork(id); err != nil {
			e.log.Warn("delete persisted network", "network", id, "err", err)
		}
	}
	for _, s := range e.sinks {
		s.NetworkRemoved(id)
	}
	return nil
}

// CloseBuffer drops a query/DM buffer from a network's state and re-broadcasts
// the network so every connected client (and tab) drops it too. It is only for
// queries: channels must be left via PART (which removes their buffer through
// the normal EvPart path) and the status buffer cannot be closed. Message
// history in the store is left intact — reopening the query (or a new message
// arriving) restores the buffer and its backlog. Safe to call from any
// goroutine. Mirrors SetBufferState's lock/notify discipline: state is mutated
// under e.mu, then notifyNetwork is called after releasing it (it RLocks).
func (e *Engine) CloseBuffer(network, buffer string) error {
	e.mu.Lock()
	n := e.user.Network(network)
	if n == nil {
		e.mu.Unlock()
		return errors.New("unknown network")
	}
	c := n.Channel(buffer)
	if c == nil {
		e.mu.Unlock()
		return errors.New("unknown buffer")
	}
	if c.Kind != KindQuery {
		e.mu.Unlock()
		return fmt.Errorf("cannot close a %s buffer", c.Kind)
	}
	n.remove(buffer)
	e.mu.Unlock()

	e.notifyNetwork(network)
	return nil
}

// UpdateNetwork changes an existing network's settings. Connection-level
// changes (server address, TLS, user/realname, SASL) require re-dialing;
// nick and channel changes are applied live (NICK/JOIN/PART) without
// dropping the connection. The network keeps its id and message history.
// Safe to call from any goroutine.
func (e *Engine) UpdateNetwork(p NetworkParams) error {
	e.mu.Lock()
	n := e.user.Network(p.ID)
	if n == nil {
		e.mu.Unlock()
		return errors.New("unknown network")
	}
	old := n.Params
	// The GUI edit form doesn't carry per-channel join keys, so preserve the
	// stored keys for channels still in the auto-join list (dropping keys for
	// channels the edit removed). This runs before both branches so a reconnect
	// dials with the keys intact.
	p.ChannelKeys = reconcileKeys(old.ChannelKeys, p.Channels)

	if needsReconnect(old, p) {
		if e.connector == nil {
			e.mu.Unlock()
			return errNoConnector
		}
		conn, err := e.connector.Dial(p, e)
		if err != nil {
			e.mu.Unlock()
			return err
		}
		if cancel := e.connCancels[p.ID]; cancel != nil {
			cancel()
			delete(e.connCancels, p.ID)
		}
		oldConn := e.conns[p.ID]
		// Clone into the tree so p stays private to this goroutine: it is
		// read below (persistAndNotify) after e.mu is released, while the
		// loop goroutine may already be mutating the live Params.
		n.Params, n.Name, n.Nick = p.clone(), p.Name, p.Nick
		e.conns[p.ID] = conn
		if e.running {
			e.startConnLocked(p.ID, conn)
		}
		e.mu.Unlock()
		if oldConn != nil {
			_ = oldConn.Close()
		}
		e.persistAndNotify(p)
		return nil
	}

	// Live update: apply nick and channel changes over the open connection.
	conn := e.conns[p.ID]
	registered := n.State == StateRegistered
	nickChanged := old.Nick != p.Nick
	added, removed := diffChannels(old.Channels, p.Channels)
	// Clone for the same reason as the reconnect branch above.
	n.Params, n.Name = p.clone(), p.Name
	e.mu.Unlock()

	if conn != nil && registered {
		if nickChanged {
			_ = conn.SendRaw("NICK " + p.Nick)
		}
		for _, ch := range removed {
			_ = conn.SendRaw("PART " + ch)
		}
		for _, ch := range added {
			_ = conn.SendRaw("JOIN " + ch)
		}
	}
	e.persistAndNotify(p)
	return nil
}

// needsReconnect reports whether the change between old and p touches a
// connection-level setting that can only take effect on a fresh connection.
func needsReconnect(old, p NetworkParams) bool {
	return old.Addr != p.Addr || old.TLS != p.TLS ||
		old.User != p.User || old.Realname != p.Realname ||
		old.SASLUser != p.SASLUser || old.SASLPass != p.SASLPass ||
		old.ServerPass != p.ServerPass || old.SASLExternal != p.SASLExternal ||
		old.CertPEM != p.CertPEM
}

// reconcileKeys keeps only the join keys whose channel is still in the
// auto-join list (case-insensitive), returning nil when none remain. Used on
// edit to carry stored keys across a GUI update that can't send them.
func reconcileKeys(keys map[string]string, channels []string) map[string]string {
	if len(keys) == 0 {
		return nil
	}
	var out map[string]string
	for k, v := range keys {
		for _, ch := range channels {
			if eqFold(ch, k) {
				if out == nil {
					out = map[string]string{}
				}
				// Store under the channel-list casing so the exact-match
				// lookup in planAutojoin (keys[ch]) hits.
				out[ch] = v
				break
			}
		}
	}
	return out
}

// diffChannels returns the channels added to and removed from the auto-join
// list (case-insensitive).
func diffChannels(old, updated []string) (added, removed []string) {
	has := func(list []string, s string) bool {
		for _, x := range list {
			if eqFold(x, s) {
				return true
			}
		}
		return false
	}
	for _, c := range updated {
		if c != "" && !has(old, c) {
			added = append(added, c)
		}
	}
	for _, c := range old {
		if c != "" && !has(updated, c) {
			removed = append(removed, c)
		}
	}
	return added, removed
}

// ListChannels asks a network for its channel list (the browser). Results
// arrive asynchronously via the sink's ChannelList. query is passed to the
// server's LIST verbatim (e.g. ">100" or "*term*"); empty lists everything.
func (e *Engine) ListChannels(network, query string) error {
	conn := e.connFor(network)
	if conn == nil {
		return errors.New("unknown or disconnected network")
	}
	e.mu.Lock()
	e.listAccum[network] = nil // reset any prior in-progress list
	e.mu.Unlock()
	cmd := "LIST"
	if query != "" {
		cmd += " " + query
	}
	return conn.SendRaw(cmd)
}

// persistAndNotify saves a network's params and pushes a fresh snapshot.
func (e *Engine) persistAndNotify(p NetworkParams) {
	if e.netStore != nil {
		if err := e.netStore.SaveNetwork(p); err != nil {
			e.log.Warn("persist network", "network", p.ID, "err", err)
		}
	}
	e.notifyNetwork(p.ID)
}

// ReorderBuffers records the user's manual buffer order for a network (names
// in display form; the status buffer may be omitted). It only persists the
// order — applied to snapshots by orderChannels — and re-pushes the network so
// every tab re-renders. Safe from any goroutine.
func (e *Engine) ReorderBuffers(network string, names []string) error {
	e.mu.Lock()
	n := e.user.Network(network)
	if n == nil {
		e.mu.Unlock()
		return errors.New("unknown network")
	}
	order := make([]string, len(names))
	for i, name := range names {
		order[i] = lower(name)
	}
	n.Params.BufferOrder = order
	p := n.Params.clone()
	e.mu.Unlock()
	e.persistAndNotify(p)
	return nil
}

// ReorderNetworks applies the user's manual network order. ids is the desired
// order; networks present in the engine but missing from ids keep their
// relative order after the listed ones, and unknown ids are ignored. It
// rewrites each network's Pos, persists them, and fans the new full order out
// to sinks. Safe from any goroutine.
func (e *Engine) ReorderNetworks(ids []string) {
	e.mu.Lock()
	cur := e.user.Networks
	seen := make(map[string]bool, len(ids))
	reordered := make([]*Network, 0, len(cur))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		if n := e.user.Network(id); n != nil {
			seen[id] = true
			reordered = append(reordered, n)
		}
	}
	for _, n := range cur { // append networks not named in ids, order preserved
		if !seen[n.ID] {
			reordered = append(reordered, n)
		}
	}
	e.user.Networks = reordered
	order := make([]string, len(reordered))
	params := make([]NetworkParams, len(reordered))
	for i, n := range reordered {
		n.Params.Pos = i
		order[i] = n.ID
		params[i] = n.Params.clone()
	}
	e.mu.Unlock()

	if e.netStore != nil {
		for _, p := range params {
			if err := e.netStore.SaveNetwork(p); err != nil {
				e.log.Warn("persist network order", "network", p.ID, "err", err)
			}
		}
	}
	for _, s := range e.sinks {
		s.NetworksReordered(order)
	}
}

// NetworkConfig returns the stored connection params for a network.
func (e *Engine) NetworkConfig(id string) (NetworkParams, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if n := e.user.Network(id); n != nil {
		// Clone: the caller reads this outside e.mu while the loop goroutine
		// keeps mutating the live Params (autojoin edits, channel keys).
		return n.Params.clone(), true
	}
	return NetworkParams{}, false
}

// SetConnected connects or disconnects a network without forgetting it.
// Disconnecting cancels its reconnect goroutine (so it stays down until
// asked), and connecting dials a fresh connection. Safe from any goroutine.
func (e *Engine) SetConnected(id string, connect bool) error {
	e.mu.Lock()
	n := e.user.Network(id)
	if n == nil {
		e.mu.Unlock()
		return errors.New("unknown network")
	}
	if connect {
		if e.connCancels[id] != nil { // already connected/connecting
			e.mu.Unlock()
			return nil
		}
		if e.connector == nil {
			e.mu.Unlock()
			return errNoConnector
		}
		conn, err := e.connector.Dial(n.Params, e)
		if err != nil {
			e.mu.Unlock()
			return err
		}
		e.conns[id] = conn
		if e.running {
			e.startConnLocked(id, conn)
		}
		e.mu.Unlock()
		return nil
	}
	// Disconnect: stop the reconnect goroutine and close the socket.
	if cancel := e.connCancels[id]; cancel != nil {
		cancel()
		delete(e.connCancels, id)
	}
	conn := e.conns[id]
	n.State = StateDisconnected
	e.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	e.notifyNetwork(id)
	return nil
}

// AddMonitor adds a nick to a network's friends list (IRCv3 MONITOR): it
// updates and persists Params.Monitor and, on a registered connection, arms the
// watch with MONITOR +. A reconnect re-arms the whole list from Params. No-op
// if the nick is already monitored.
func (e *Engine) AddMonitor(networkID, nick string) error {
	nick = strings.TrimSpace(nick)
	if nick == "" {
		return errors.New("empty nick")
	}
	e.mu.Lock()
	n := e.user.Network(networkID)
	if n == nil {
		e.mu.Unlock()
		return errors.New("unknown network")
	}
	if slices.ContainsFunc(n.Params.Monitor, func(s string) bool { return eqFold(s, nick) }) {
		e.mu.Unlock()
		return nil
	}
	n.Params.Monitor = append(n.Params.Monitor, strings.ToLower(nick))
	conn := e.conns[networkID]
	registered := n.State == StateRegistered
	p := n.Params.clone()
	e.mu.Unlock()

	if conn != nil && registered {
		_ = conn.SendRaw("MONITOR + " + nick)
	}
	e.persistAndNotify(p)
	return nil
}

// RemoveMonitor drops a nick from the friends list, clearing its live presence
// and sending MONITOR - on a registered connection. No-op if not monitored.
func (e *Engine) RemoveMonitor(networkID, nick string) error {
	e.mu.Lock()
	n := e.user.Network(networkID)
	if n == nil {
		e.mu.Unlock()
		return errors.New("unknown network")
	}
	before := len(n.Params.Monitor)
	n.Params.Monitor = slices.DeleteFunc(n.Params.Monitor, func(s string) bool { return eqFold(s, nick) })
	if len(n.Params.Monitor) == before {
		e.mu.Unlock()
		return nil
	}
	delete(n.MonitorOnline, strings.ToLower(nick))
	conn := e.conns[networkID]
	registered := n.State == StateRegistered
	p := n.Params.clone()
	e.mu.Unlock()

	if conn != nil && registered {
		_ = conn.SendRaw("MONITOR - " + nick)
	}
	e.persistAndNotify(p)
	return nil
}

// connFor returns the connection for a network id, race-safely.
func (e *Engine) connFor(id string) IRCConn {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.conns[id]
}

// SendInput submits a line of user input for a buffer. It is goroutine-safe
// (callable from server handlers). A leading slash makes it a command
// (handled by a plugin hook_command or a built-in); "//" escapes to a
// literal message. Otherwise it is an outbound message: it runs through
// hooks, is sent to IRC, and is echoed locally (echo-message is off, so the
// local echo is how the sender sees their own line).
func (e *Engine) SendInput(network, buffer, text string) {
	e.sendInput(network, buffer, text, 0)
}

func (e *Engine) sendInput(network, buffer, text string, depth int) {
	if strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "//") {
		rest := strings.TrimPrefix(text, "/")
		name, argstr, _ := strings.Cut(rest, " ")
		if name != "" {
			// Expand a command alias, then re-process the result (guarded
			// against alias loops). sendInput runs on a server goroutine, so the
			// table is read under the lock SetAliases writes it with.
			e.mu.RLock()
			tmpl, ok := e.aliases[strings.ToLower(name)]
			e.mu.RUnlock()
			if ok && depth < 8 {
				e.sendInput(network, buffer, expandAlias(tmpl, strings.Fields(argstr)), depth+1)
				return
			}
			e.HandleEvent(Event{
				Type: EvCommand, Network: network, Buffer: buffer, Time: time.Now(),
				Command: name, Args: strings.Fields(argstr), Text: argstr,
			})
			return
		}
	}
	text = strings.TrimPrefix(text, "/") // "//foo" → "/foo"
	e.HandleEvent(Event{
		Type:    EvMessageOut,
		Network: network,
		Time:    time.Now(),
		Message: &Message{
			Network: network,
			Buffer:  buffer,
			Time:    time.Now(),
			Kind:    MsgPrivmsg,
			Text:    text,
			Self:    true,
		},
	})
}

// Snapshot returns a deep copy of the user state, safe to read from any
// goroutine. Used by the server to build the init snapshot.
func (e *Engine) Snapshot() *User {
	e.mu.RLock()
	defer e.mu.RUnlock()
	u := e.user.clone()
	for _, n := range u.Networks {
		if conn := e.conns[n.ID]; conn != nil {
			n.Caps = conn.Caps()
		}
		orderChannels(n)
	}
	return u
}

// HandleEvent implements ConnHandler: it enqueues an inbound event onto the
// bus. Safe to call from any goroutine; drops events after shutdown.
func (e *Engine) HandleEvent(ev Event) {
	select {
	case e.events <- ev:
	case <-e.done:
	}
}

// Run starts the event loop and a goroutine per connection, then blocks
// until ctx is cancelled, after which it closes connections and drains.
// Networks added at runtime (AddNetwork) start their own goroutines.
func (e *Engine) Run(ctx context.Context) error {
	e.mu.Lock()
	e.running = true
	e.runCtx = ctx
	e.runWG.Go(func() { e.loop(ctx) })
	for id, conn := range e.conns {
		e.startConnLocked(id, conn)
	}
	e.mu.Unlock()

	<-ctx.Done()
	e.closeo.Do(func() { close(e.done) })

	e.mu.Lock()
	e.running = false
	conns := make([]IRCConn, 0, len(e.conns))
	for _, conn := range e.conns {
		conns = append(conns, conn)
	}
	e.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}

	e.runWG.Wait()
	if err := e.host.Close(); err != nil {
		e.log.Warn("closing plugin host", "err", err)
	}
	return nil
}

// startConnLocked launches a connection's reconnect goroutine under a child
// context so it can be cancelled individually (on RemoveNetwork) or by Run's
// context. The caller holds e.mu.
func (e *Engine) startConnLocked(id string, conn IRCConn) {
	connCtx, cancel := context.WithCancel(e.runCtx)
	e.connCancels[id] = cancel
	e.runWG.Go(func() { e.runConn(connCtx, id, conn) })
}

// runConn drives one connection with simple exponential reconnect backoff.
// Phase 4 hardens the stays-connected behavior; this keeps the daemon alive
// across transient drops in the meantime.
const (
	baseBackoff = time.Second
	maxBackoff  = 30 * time.Second
	// stableFor is how long a connection must last before we treat it as
	// healthy and reset the backoff. Longer than a handshake-then-RST so a
	// flapping server keeps growing its delay, but short enough that a
	// genuinely-up link recovers a fast first retry on its next drop.
	stableFor = 60 * time.Second
)

// reconnectDelay decides how long to wait before the next dial. backoff is the
// current delay and lasted is how long the connection that just ended stayed
// up. A connection that lasted at least stableFor reflects a healthy link, not
// the recent instability the backoff throttles, so its delay resets to base.
// Returns the duration to sleep and the backoff to carry into the next round.
func reconnectDelay(backoff, lasted time.Duration) (sleep, next time.Duration) {
	if lasted >= stableFor {
		backoff = baseBackoff
	}
	return backoff, min(backoff*2, maxBackoff)
}

func (e *Engine) runConn(ctx context.Context, id string, conn IRCConn) {
	backoff := baseBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		e.HandleEvent(Event{Type: evSetState, Network: id, State: StateConnecting})
		start := time.Now()
		err := conn.Connect(ctx)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, ErrAuthFailed) {
			// Bad credentials don't heal with retries — they hammer the
			// server (and services) with the same wrong password. Park the
			// network; editing it or /connect dials a fresh connection.
			e.log.Warn("authentication failed; not retrying", "network", id, "err", err)
			e.HandleEvent(Event{Type: evSetState, Network: id, State: StateDisconnected})
			e.HandleEvent(Event{
				Type: EvNumeric, Network: id,
				Text: "authentication failed — check the network's SASL credentials, then save or /connect to retry",
			})
			return
		}
		var sleep time.Duration
		sleep, backoff = reconnectDelay(backoff, time.Since(start))
		e.log.Warn("connection ended; will retry", "network", id, "err", err, "backoff", sleep)
		select {
		case <-ctx.Done():
			return
		case <-time.After(sleep):
		}
	}
}

// loop is the single goroutine that mutates state. Every event passes
// through here in arrival order.
func (e *Engine) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-e.events:
			e.handle(ctx, ev)
		}
	}
}

// handle dispatches an event to plugin hooks, then commits it. The hook
// contract varies by event type:
//
//   - message in/out: hooks may rewrite or drop (keep=false) the message.
//   - command: a plugin that handles the command returns keep=false; if no
//     plugin claims it, the engine runs its built-in command handler.
//   - signals: hooks are notified; the result is ignored.
//
// Internal events (state, print) bypass the host entirely.
func (e *Engine) handle(ctx context.Context, ev Event) {
	switch ev.Type {
	case evSetState:
		e.apply(ev)

	case EvMessageIn, EvMessageOut, EvTopic:
		// Topics, like messages, are dispatched as rewritable: a plugin may
		// change the topic text before it's committed (e.g. fish decrypts an
		// encrypted topic). The host still notifies "topic" signal hooks.
		out, keep := e.host.Dispatch(ctx, ev)
		if !keep {
			return // a hook dropped it
		}
		e.apply(out)

	case EvCommand:
		if _, keep := e.host.Dispatch(ctx, ev); !keep {
			return // a plugin command consumed it
		}
		e.runBuiltinCommand(ev)

	default: // join, part, quit, nick, connect, disconnect
		e.host.Dispatch(ctx, ev) // notify-only
		e.apply(ev)
		if ev.Type == EvConnect {
			e.runPerform(ev.Network)
		}
	}
}

const performCommandDelay = time.Second

// runPerform replays a network's configured perform commands after it
// registers, on every (re)connect, then starts configured channel auto-join.
// Commands are spaced out so service authentication/user modes have time to
// settle (QuakeNet, for example, may otherwise see JOIN before +x). Lines go
// through the normal input path (aliases, commands, plugin hooks), exactly as
// if typed in the status buffer. Networks without Perform auto-join at once.
func (e *Engine) runPerform(network string) {
	e.mu.RLock()
	n := e.user.Network(network)
	var lines []string
	var variables map[string]string
	var conn IRCConn
	var hasAutojoin bool
	if n != nil && len(n.Params.Perform) > 0 {
		lines = append(lines, n.Params.Perform...)
		networkName := n.Name
		if networkName == "" {
			networkName = n.ID
		}
		variables = map[string]string{
			"me":       n.Nick,
			"nick":     n.Nick,
			"network":  networkName,
			"server":   n.Params.Addr,
			"user":     n.Params.User,
			"realname": n.Params.Realname,
		}
	}
	if n != nil {
		hasAutojoin = len(n.Params.Channels) > 0
		conn = e.conns[network]
	}
	e.mu.RUnlock()
	if conn == nil {
		return
	}

	e.mu.Lock()
	e.startupSeq[network]++
	seq := e.startupSeq[network]
	e.mu.Unlock()

	if len(lines) == 0 {
		if e.startupActive(network, seq, conn) {
			e.autojoinOrDefer(network, seq, conn)
		}
		return
	}
	go func() {
		commands := make([]string, 0, len(lines))
		for _, line := range lines {
			if strings.TrimSpace(line) != "" {
				commands = append(commands, line)
			}
		}
		for i, line := range commands {
			if !e.startupActive(network, seq, conn) {
				return
			}
			e.SendInput(network, StatusBuffer, expandPerformVariables(line, variables))
			// Wait between Perform commands, and after the final command when
			// channels are waiting to auto-join. No trailing wait is needed if
			// there is no subsequent startup action.
			if i+1 < len(commands) || hasAutojoin {
				select {
				case <-e.done:
					return
				case <-time.After(performCommandDelay):
				}
			}
		}
		if e.startupActive(network, seq, conn) {
			e.autojoinOrDefer(network, seq, conn)
		}
	}()
}

// startupActive prevents a delayed Perform sequence from leaking across a
// disconnect/reconnect or a live network edit that replaces the connection.
func (e *Engine) startupActive(network string, seq uint64, conn IRCConn) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.startupActiveLocked(network, seq, conn)
}

// startupActiveLocked is startupActive for callers already holding e.mu.
func (e *Engine) startupActiveLocked(network string, seq uint64, conn IRCConn) bool {
	n := e.user.Network(network)
	return n != nil && n.State == StateRegistered && e.conns[network] == conn && e.startupSeq[network] == seq
}

// joinHoldTimeout is how long a HoldJoins gate may stay unreleased before the
// engine auto-joins anyway. A buggy plugin, unreachable services, or a failed
// auth must never leave the user outside every channel indefinitely.
const joinHoldTimeout = 45 * time.Second

// joinGate is one network's held-back startup auto-join (see Engine.joinGates).
// Fields are guarded by e.mu; timer auto-releases an abandoned hold.
type joinGate struct {
	// pending is set when startup reached the auto-join point while the gate
	// was held; releasing then flushes the join. seq/conn are recorded at that
	// point so the flush is guarded like any startup action — a gate outlived
	// by a reconnect or a live network edit can't join on the wrong connection.
	pending bool
	seq     uint64
	conn    IRCConn
	timer   *time.Timer
}

// HoldJoins parks the network's startup channel auto-join until ReleaseJoins
// (or the joinHoldTimeout fallback). Meant to be called from a plugin's
// connect hook — the engine loop dispatches that before it starts the
// Perform/auto-join sequence — so an auth plugin can finish an asynchronous
// service login (QuakeNet Q, NickServ without SASL) and set MODE +x before
// any JOIN exposes the real host. Holding again restarts the gate and its
// fallback timer.
func (e *Engine) HoldJoins(network string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.user.Network(network) == nil {
		return fmt.Errorf("unknown network %q", network)
	}
	if old := e.joinGates[network]; old != nil {
		old.timer.Stop()
	}
	g := &joinGate{}
	g.timer = time.AfterFunc(joinHoldTimeout, func() { e.expireJoinGate(network, g) })
	e.joinGates[network] = g
	return nil
}

// ReleaseJoins lifts a HoldJoins gate and sends the parked auto-join, if
// startup already reached it. The plugin sends its MODE +x before releasing
// and the server processes a connection's commands in order, so the mask is
// active by the time the JOINs land. Releasing without a hold is a harmless
// no-op, so a plugin may release unconditionally once auth settles.
func (e *Engine) ReleaseJoins(network string) error {
	e.mu.Lock()
	if e.user.Network(network) == nil {
		e.mu.Unlock()
		return fmt.Errorf("unknown network %q", network)
	}
	conn := e.takeJoinGateLocked(network)
	e.mu.Unlock()
	if conn != nil {
		conn.Autojoin()
	}
	return nil
}

// expireJoinGate is the fallback path: g's timer fired without ReleaseJoins.
// Only the currently-installed gate may act — a stale timer beaten by a
// re-hold or a disconnect reset finds a different (or no) gate and does
// nothing.
func (e *Engine) expireJoinGate(network string, g *joinGate) {
	e.mu.Lock()
	if e.joinGates[network] != g {
		e.mu.Unlock()
		return
	}
	conn := e.takeJoinGateLocked(network)
	e.mu.Unlock()
	e.log.Warn("join hold expired without release", "network", network, "timeout", joinHoldTimeout)
	if conn != nil {
		conn.Autojoin()
	}
}

// takeJoinGateLocked removes network's gate and reports the connection to
// auto-join on: non-nil only when startup already parked a join on the gate
// and that startup sequence is still the live one. Caller holds e.mu.
func (e *Engine) takeJoinGateLocked(network string) IRCConn {
	g := e.joinGates[network]
	if g == nil {
		return nil
	}
	delete(e.joinGates, network)
	g.timer.Stop()
	if g.pending && e.startupActiveLocked(network, g.seq, g.conn) {
		return g.conn
	}
	return nil
}

// autojoinOrDefer sends the startup auto-join, unless a plugin holds the join
// gate — then the join is parked on the gate for ReleaseJoins (or the fallback
// timer) to flush.
func (e *Engine) autojoinOrDefer(network string, seq uint64, conn IRCConn) {
	e.mu.Lock()
	if g := e.joinGates[network]; g != nil {
		g.pending, g.seq, g.conn = true, seq, conn
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()
	conn.Autojoin()
}

// expandPerformVariables substitutes named $variables in a perform line.
// ${variable} disambiguates a variable from adjacent text, and $$ emits a
// literal dollar sign.
// Unknown variables are deliberately preserved so adding variables later does
// not silently corrupt existing commands (and literal IRC text containing a
// dollar-prefixed word continues to work).
func expandPerformVariables(line string, variables map[string]string) string {
	var b strings.Builder
	for i := 0; i < len(line); {
		if line[i] != '$' {
			b.WriteByte(line[i])
			i++
			continue
		}

		if i+1 == len(line) {
			b.WriteByte('$')
			break
		}
		if line[i+1] == '$' {
			b.WriteByte('$')
			i += 2
			continue
		}
		if line[i+1] == '{' {
			closeOffset := strings.IndexByte(line[i+2:], '}')
			if closeOffset < 0 {
				b.WriteByte('$')
				i++
				continue
			}
			end := i + 2 + closeOffset
			name := line[i+2 : end]
			if value, ok := variables[name]; ok {
				b.WriteString(value)
			} else {
				b.WriteString(line[i : end+1])
			}
			i = end + 1
			continue
		}

		end := i + 1
		for end < len(line) {
			c := line[end]
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') &&
				(c < '0' || c > '9') && c != '_' {
				break
			}
			end++
		}
		name := line[i+1 : end]
		if value, ok := variables[name]; ok {
			b.WriteString(value)
		} else {
			b.WriteString(line[i:end])
		}
		i = end
	}
	return b.String()
}

// apply commits an event: it mutates state under the write lock, then
// performs I/O (sink fan-out) with the lock released.
func (e *Engine) apply(ev Event) {
	// LIST results accumulate, then flush to observers as one batch.
	switch ev.Type {
	case EvListItem:
		e.mu.Lock()
		if len(e.listAccum[ev.Network]) < maxListItems {
			e.listAccum[ev.Network] = append(e.listAccum[ev.Network], ChannelListItem{
				Name: ev.Buffer, Users: ev.Count, Topic: ev.Text,
			})
		}
		e.mu.Unlock()
		return
	case EvListEnd:
		e.mu.Lock()
		items := e.listAccum[ev.Network]
		delete(e.listAccum, ev.Network)
		e.mu.Unlock()
		for _, s := range e.sinks {
			s.ChannelList(ev.Network, items)
		}
		return
	case EvMessageOut:
		e.applyMessageOut(ev)
		return
	case EvTyping:
		for _, s := range e.sinks {
			s.Typing(ev.Network, ev.Buffer, ev.Nick, ev.Text)
		}
		return
	case EvReact:
		for _, s := range e.sinks {
			s.React(ev.Network, ev.Buffer, ev.Target, ev.Nick, ev.Text)
		}
		return
	case EvRedact:
		for _, s := range e.sinks {
			s.Redact(ev.Network, ev.Buffer, ev.Target, ev.Nick, ev.Text)
		}
		return
	case EvNumeric:
		e.applyNumeric(ev)
		return
	}

	e.mu.Lock()
	emit, netChanged, persist := e.applyLocked(ev)
	// Snapshot the params to persist while still holding the lock, so the
	// write-back below sees a stable copy and not a concurrently-mutated one.
	var params NetworkParams
	if persist && e.netStore != nil {
		if n := e.user.Network(ev.Network); n != nil {
			params = n.Params.clone()
		} else {
			persist = false
		}
	}
	e.mu.Unlock()

	for _, m := range emit {
		e.broadcast(m)
	}
	if persist && e.netStore != nil {
		if err := e.netStore.SaveNetwork(params); err != nil {
			e.log.Warn("persist autojoin", "network", ev.Network, "err", err)
		}
	}
	if netChanged {
		e.notifyNetwork(ev.Network)
	}
}

// recordPendingKeys parses a /join argument string ("#a,#b k1,k2") and stashes
// each channel's key so it can be committed to the persisted auto-join list
// when the server confirms the self-JOIN. Keys are positional; channels
// without a key contribute no entry.
func (e *Engine) recordPendingKeys(network, text string) {
	chans, keys, _ := strings.Cut(strings.TrimSpace(text), " ")
	chanList := strings.Split(chans, ",")
	keyList := strings.Split(strings.TrimSpace(keys), ",")
	for i, ch := range chanList {
		ch = strings.TrimSpace(ch)
		if ch == "" || i >= len(keyList) {
			continue
		}
		if key := strings.TrimSpace(keyList[i]); key != "" {
			e.pendingKeys[network+"\t"+lower(ch)] = key
		}
	}
}

// takePendingKey returns and clears any join key recorded for a channel by a
// preceding /join. ok reports whether a key was pending (always a non-empty
// key when true). Called from applyLocked on the loop goroutine.
func (e *Engine) takePendingKey(network, channel string) (key string, ok bool) {
	k := network + "\t" + lower(channel)
	key, ok = e.pendingKeys[k]
	if ok {
		delete(e.pendingKeys, k)
	}
	return key, ok
}

// whoisKey builds the pendingWhois map key for a network+nick pair. The nick
// is folded with lower (ASCII, matching the member-map keys) so a reply pairs
// to its request regardless of the casing the server echoes back.
func whoisKey(network, nick string) string {
	return network + "\t" + lower(nick)
}

// applyNumeric routes a server numeric (WHOIS/WHO/WHOWAS reply, an error
// code, …) into the buffer that issued the request when we can pair them,
// else into the per-network status buffer. pendingWhois is keyed by
// "<network>\t<lowercase-nick>" and cleared on the matching end-of marker
// so a long-lived plugin doesn't leak entries.
func (e *Engine) applyNumeric(ev Event) {
	key := whoisKey(ev.Network, ev.Nick)
	buf := StatusBuffer
	b, pending := e.pendingWhois[key]
	if b != "" {
		buf = b
	}
	// girc automatically issues WHO/WHOX after we join a channel to hydrate
	// its internal member state. JOIN also naturally produces an end-of-NAMES
	// reply. These are background protocol bookkeeping, not user lookups, so
	// keep their numerics silent unless a matching /who or /names command was
	// recorded by startNumeric/the names handler.
	if !pending {
		switch ev.Count {
		case 352, 354, 315, 366: // WHO, WHOX, end-of-WHO, end-of-NAMES
			return
		}
	}
	switch ev.Count {
	case 318, 369, 315: // RPL_ENDOFWHOIS / WHOWAS / WHO
		delete(e.pendingWhois, key)
	case 366: // RPL_ENDOFNAMES
		delete(e.pendingWhois, key)
	}
	e.inject(Message{
		Network: ev.Network, Buffer: buf, Time: ev.Time,
		Kind: MsgSystem, Text: ev.Text,
	})
}

// applyMessageOut sends an outbound message to IRC and locally echoes it —
// unless the connection negotiated echo-message, in which case the server's
// own echo (an EvMessageIn with Self set) is the displayed copy, so we skip
// the local one to avoid a duplicate.
func (e *Engine) applyMessageOut(ev Event) {
	if ev.Message == nil {
		return
	}
	echo := e.echoMessage(ev.Network)

	e.mu.Lock()
	n := e.user.Network(ev.Network)
	if n == nil {
		e.mu.Unlock()
		return
	}
	m := *ev.Message
	if m.From == "" {
		m.From = n.Nick
	}
	_, created := n.getOrCreate(m.Buffer, bufferKind(m.Buffer))
	e.mu.Unlock()

	if conn := e.connFor(ev.Network); conn != nil {
		if err := conn.Message(m.Buffer, m.Text); err != nil {
			e.log.Warn("send failed", "network", ev.Network, "err", err)
		}
	}
	if !echo {
		e.broadcast(m)
	}
	if created {
		e.notifyNetwork(ev.Network)
	}
}

// SendTyping sends a typing notification (state active/paused/done) to a
// buffer as a +typing TAGMSG, if the network negotiated message-tags.
func (e *Engine) SendTyping(network, buffer, state string) {
	if state != "active" && state != "paused" && state != "done" {
		return
	}
	conn := e.connWithCap(network, "message-tags")
	if conn == nil {
		return
	}
	_ = conn.SendRaw("@+typing=" + state + " TAGMSG " + buffer)
}

// SendReaction sends an emoji reaction to a message (target msgid) in a
// buffer as a +draft/react TAGMSG carrying +draft/reply, when the network
// negotiated message-tags. The server echoes it back (if it broadcasts to
// the reactor), which is how the sender's own reaction appears.
func (e *Engine) SendReaction(network, buffer, target, reaction string) {
	if target == "" || reaction == "" {
		return
	}
	conn := e.connWithCap(network, "message-tags")
	if conn == nil {
		return
	}
	_ = conn.SendRaw("@+draft/react=" + reaction + ";+draft/reply=" + target + " TAGMSG " + buffer)
}

// SendRedact redacts a message (target msgid) in a buffer via the
// draft/message-redaction REDACT command, when the network negotiated it.
func (e *Engine) SendRedact(network, buffer, target, reason string) {
	if target == "" {
		return
	}
	conn := e.connWithCap(network, "draft/message-redaction")
	if conn == nil {
		return
	}
	line := "REDACT " + buffer + " " + target
	if reason != "" {
		line += " :" + reason
	}
	_ = conn.SendRaw(line)
}

// echoMessage reports whether the network's connection negotiated the
// echo-message capability (so the server will echo our sent lines back).
func (e *Engine) echoMessage(network string) bool {
	return e.hasCap(network, "echo-message")
}

// connWithCap returns the network's connection if it negotiated capability
// cap, else nil. It folds the connFor-nil-check and the Caps() membership
// test that the typing/reaction/redaction send paths all share.
func (e *Engine) connWithCap(network, cap string) IRCConn {
	conn := e.connFor(network)
	if conn == nil || !slices.Contains(conn.Caps(), cap) {
		return nil
	}
	return conn
}

// hasCap reports whether the network's connection negotiated capability cap.
func (e *Engine) hasCap(network, cap string) bool {
	return e.connWithCap(network, cap) != nil
}

// notifyNetwork pushes a fresh snapshot of one network to all sinks.
func (e *Engine) notifyNetwork(id string) {
	nc := e.SnapshotNetwork(id)
	if nc == nil {
		return
	}
	for _, s := range e.sinks {
		s.NetworkChanged(nc)
	}
}

// inject commits a synthetic line (from a plugin's Print, or a local echo of
// a plugin-sent message) directly to state and sinks, bypassing the event
// bus so it never recurses into plugin hooks. Safe to call from the plugin
// goroutine, which runs while the engine loop is outside the state lock.
func (e *Engine) inject(m Message) {
	e.mu.Lock()
	n := e.user.Network(m.Network)
	created := false
	if n != nil {
		_, created = n.getOrCreate(m.Buffer, bufferKind(m.Buffer))
	}
	e.mu.Unlock()
	if n == nil {
		return
	}
	e.broadcast(m)
	if created {
		e.notifyNetwork(m.Network)
	}
}

// applyLocked mutates state for ev and returns the buffer lines to emit,
// whether the network's structure changed (so a snapshot should be pushed to
// observers), and whether the network's persisted params changed (so they
// should be written back to the store — e.g. the auto-join list after we join
// or part a channel). The caller holds e.mu for writing. EvMessageOut is
// handled separately (see applyMessageOut) because it needs the echo-message
// capability check, which can't take the lock that this holds.
func (e *Engine) applyLocked(ev Event) (emit []Message, netChanged, persist bool) {
	n := e.user.Network(ev.Network)
	if n == nil {
		return nil, false, false
	}
	sys := func(buffer, text string) {
		emit = append(emit, Message{
			Network: n.ID, Buffer: buffer, Time: time.Now(),
			Kind: MsgSystem, Text: text,
		})
	}
	// line emits with a specific kind (join/part/quit/nick) so the client can
	// recognize membership churn and fold it; sys() stays for true system
	// notices (connect/disconnect/topic/numerics).
	line := func(kind MsgKind, buffer, from, text string) {
		emit = append(emit, Message{
			Network: n.ID, Buffer: buffer, Time: time.Now(),
			From: from, Kind: kind, Text: text,
		})
	}

	switch ev.Type {
	case evSetState:
		n.State = ev.State
		netChanged = true

	case EvConnect:
		n.State = StateRegistered
		if ev.Nick != "" {
			n.Nick = ev.Nick
		}
		sys(StatusBuffer, fmt.Sprintf("connected to %s as %s", n.Name, n.Nick))
		netChanged = true

	case EvDisconnect:
		n.State = StateDisconnected
		msg := "disconnected"
		if ev.Text != "" {
			msg += ": " + ev.Text
		}
		sys(StatusBuffer, msg)
		// Reset the join gate: the next registration starts clean, and a hold
		// (or its fallback timer) left over from this session must not park or
		// flush a JOIN on the new connection.
		if g := e.joinGates[ev.Network]; g != nil {
			g.timer.Stop()
			delete(e.joinGates, ev.Network)
		}
		// Drop stale friend presence: a dropped connection knows nothing about
		// who is online until MONITOR re-reports after the next registration.
		n.MonitorOnline = nil
		// Drop stale channel membership too. A dropped connection misses the
		// QUIT/PART traffic that would normally retire members (including our
		// own pre-disconnect nick once the server pings out its ghost), and the
		// NAMES burst on rejoin only adds members, never removes them. Clearing
		// here lets the next registration's NAMES + JOINs rebuild each list from
		// scratch, so no phantom survives a reconnect.
		for _, c := range n.Channels {
			if len(c.Members) > 0 {
				c.Members = map[string]*Member{}
			}
		}
		netChanged = true

	case EvMessageIn:
		if ev.Message == nil {
			return emit, false, false
		}
		m := *ev.Message
		if !m.Self && (m.Kind == MsgPrivmsg || m.Kind == MsgNotice || m.Kind == MsgAction) {
			m.Highlight = e.highlight.Match(m.Text, n.Nick)
		}
		c, created := n.getOrCreate(m.Buffer, bufferKind(m.Buffer))
		if created {
			e.applyPendingStateLocked(ev.Network, c)
		}
		emit = append(emit, m)
		netChanged = created

	case EvNames:
		// The server's NAMES reply on join: merge the listed members
		// without emitting join lines.
		c, _ := n.getOrCreate(ev.Buffer, KindChannel)
		for _, m := range ev.Members {
			mc := m
			c.Members[lower(m.Nick)] = &mc
		}
		netChanged = true

	case EvAway:
		// away-notify: update the member's away flag in every channel we
		// share, without a system line.
		for _, c := range n.Channels {
			if m, ok := c.Members[lower(ev.Nick)]; ok {
				m.Away = ev.Away
				netChanged = true
			}
		}

	case EvAccount:
		// account-notify: update the member's services account in every
		// channel we share, without a system line.
		for _, c := range n.Channels {
			if m, ok := c.Members[lower(ev.Nick)]; ok {
				m.Account = ev.Account
				netChanged = true
			}
		}

	case EvInvite:
		if eqFold(ev.NewNick, n.Nick) {
			sys(StatusBuffer, fmt.Sprintf("%s invited you to %s — /join %s to accept", ev.Nick, ev.Buffer, ev.Buffer))
		} else if n.Channel(ev.Buffer) != nil {
			// invite-notify: an op sees other people's invites to the channel.
			sys(ev.Buffer, fmt.Sprintf("%s invited %s to %s", ev.Nick, ev.NewNick, ev.Buffer))
		}

	case EvMonitor:
		// MONITOR reply (730 online / 731 offline): record the presence of each
		// reported nick that is actually on our friends list. The fresh snapshot
		// pushed on netChanged carries the update to the client.
		if n.MonitorOnline == nil {
			n.MonitorOnline = make(map[string]bool)
		}
		friends := make(map[string]bool, len(n.Params.Monitor))
		for _, f := range n.Params.Monitor {
			friends[lower(f)] = true
		}
		for _, nk := range ev.Args {
			lk := lower(nk)
			if friends[lk] {
				n.MonitorOnline[lk] = ev.Online
				netChanged = true
			}
		}

	case EvJoin:
		c, created := n.getOrCreate(ev.Buffer, KindChannel)
		if created {
			e.applyPendingStateLocked(ev.Network, c)
		}
		c.Members[lower(ev.Nick)] = &Member{Nick: ev.Nick, Account: ev.Account}
		line(MsgJoin, ev.Buffer, ev.Nick, fmt.Sprintf("%s has joined %s", ev.Nick, ev.Buffer))
		netChanged = true
		// When we join a channel ourselves, remember it (and the join key, if
		// the /join carried one) so it is rejoined on the next (re)connect.
		if eqFold(ev.Nick, n.Nick) {
			key, hasKey := e.takePendingKey(ev.Network, ev.Buffer)
			if n.addAutojoin(ev.Buffer, key, hasKey) {
				persist = true
			}
		}

	case EvPart:
		// If we are the one parting, drop the buffer entirely and forget its
		// auto-join entry; otherwise just remove the member.
		if eqFold(ev.Nick, n.Nick) {
			n.remove(ev.Buffer)
			if n.removeAutojoin(ev.Buffer) {
				persist = true
			}
		} else if c := n.Channel(ev.Buffer); c != nil {
			delete(c.Members, lower(ev.Nick))
		}
		text := fmt.Sprintf("%s has left %s", ev.Nick, ev.Buffer)
		if ev.Text != "" {
			text += " (" + ev.Text + ")"
		}
		line(MsgPart, ev.Buffer, ev.Nick, text)
		netChanged = true

	case EvKick:
		// Unlike a self-part, a self-kick keeps the buffer (so the reason
		// stays visible) and the auto-join entry (a kick is not the user
		// choosing to leave; the next reconnect or /join takes them back).
		// The member list is no longer valid either way.
		text := fmt.Sprintf("%s was kicked from %s by %s", ev.Nick, ev.Buffer, ev.Kicker)
		if eqFold(ev.Nick, n.Nick) {
			text = fmt.Sprintf("you were kicked from %s by %s", ev.Buffer, ev.Kicker)
			if c := n.Channel(ev.Buffer); c != nil {
				clear(c.Members)
			}
		} else if c := n.Channel(ev.Buffer); c != nil {
			delete(c.Members, lower(ev.Nick))
		}
		if ev.Text != "" {
			text += " (" + ev.Text + ")"
		}
		line(MsgPart, ev.Buffer, ev.Nick, text)
		netChanged = true

	case EvQuit:
		text := ev.Nick + " has quit"
		if ev.Text != "" {
			text += " (" + ev.Text + ")"
		}
		for _, c := range n.Channels {
			if _, ok := c.Members[lower(ev.Nick)]; ok {
				delete(c.Members, lower(ev.Nick))
				line(MsgQuit, c.Name, ev.Nick, text)
				netChanged = true
			}
		}

	case EvNick:
		if eqFold(n.Nick, ev.Nick) {
			n.Nick = ev.NewNick
			netChanged = true
		}
		for _, c := range n.Channels {
			if mem, ok := c.Members[lower(ev.Nick)]; ok {
				delete(c.Members, lower(ev.Nick))
				mem.Nick = ev.NewNick
				c.Members[lower(ev.NewNick)] = mem
				line(MsgNick, c.Name, ev.Nick, fmt.Sprintf("%s is now known as %s", ev.Nick, ev.NewNick))
				netChanged = true
			}
		}

	case EvMode:
		// A channel MODE change: update the membership prefixes it touches and
		// note it in the buffer. The fresh snapshot pushed on netChanged is what
		// re-ranks the nicklist (and re-enables op-only actions in the client).
		c := n.Channel(ev.Buffer)
		if c == nil {
			return emit, false, false
		}
		for _, mm := range ev.MemberModes {
			m, ok := c.Members[lower(mm.Nick)]
			if !ok {
				continue
			}
			if mm.Add {
				m.Modes = addPrefix(m.Modes, mm.Symbol)
			} else {
				m.Modes = strings.ReplaceAll(m.Modes, mm.Symbol, "")
			}
			netChanged = true
		}
		if ev.Nick == "" { // 324 RPL_CHANNELMODEIS
			c.Mode = ev.Text
			netChanged = true
		} else if ev.Text != "" {
			c.Mode = updateChannelMode(c.Mode, ev.Text)
			sys(ev.Buffer, fmt.Sprintf("%s sets mode %s", ev.Nick, ev.Text))
			netChanged = true
		}

	case EvTopic:
		c, _ := n.getOrCreate(ev.Buffer, KindChannel)
		if ev.Text != "" {
			c.Topic = ev.Text
		}
		if ev.Nick != "" {
			c.TopicSetter = ev.Nick
			if !ev.Time.IsZero() {
				c.TopicTime = ev.Time
			}
			if ev.Text != "" {
				sys(ev.Buffer, fmt.Sprintf("%s set topic: %s", ev.Nick, ev.Text))
			}
		}
		netChanged = true
	}
	return emit, netChanged, persist
}

// broadcast fans a committed line out to every registered sink.
func (e *Engine) broadcast(m Message) {
	if m.ID == "" {
		m.ID = e.synthID()
	}
	for _, s := range e.sinks {
		s.Print(m)
	}
}

// synthID mints a stable, process-unique id for a line the IRC server
// delivered without a msgid tag (IRCv3 message-ids is widely unsupported, so
// most PRIVMSGs and all our own status lines arrive with an empty ID). It is
// assigned here at the single fan-out point so the live frame, the persisted
// store row, and the later backlog copy all carry the *same* id. The client
// dedups backlog against the live tail by id, so an empty id there made the
// same message appear twice when a buffer was opened after accumulating live
// lines; a stable id also lets jump-to-message and search target the line.
func (e *Engine) synthID() string {
	return "loc-" + e.idBase + "-" + strconv.FormatUint(e.idSeq.Add(1), 36)
}

// SnapshotNetwork returns a deep copy of one network's state, or nil if it
// is unknown. Safe to call from any goroutine.
func (e *Engine) SnapshotNetwork(id string) *Network {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if n := e.user.Network(id); n != nil {
		nc := n.clone()
		if conn := e.conns[id]; conn != nil {
			nc.Caps = conn.Caps()
		}
		orderChannels(nc)
		return nc
	}
	return nil
}

// orderChannels stable-sorts a (cloned) network's channels into the user's
// manual buffer order (n.Params.BufferOrder), matched case-insensitively by
// name. Buffers not listed — freshly joined channels, the status buffer —
// keep their relative order after the listed ones. Mutates nc in place; only
// ever called on snapshot copies, never the live engine slice.
func orderChannels(nc *Network) {
	order := nc.Params.BufferOrder
	if len(order) == 0 || len(nc.Channels) < 2 {
		return
	}
	rank := make(map[string]int, len(order))
	for i, name := range order {
		if _, dup := rank[name]; !dup {
			rank[name] = i
		}
	}
	pos := func(c *Channel) int {
		if i, ok := rank[lower(c.Name)]; ok {
			return i
		}
		return len(order) // unlisted → after everything listed
	}
	slices.SortStableFunc(nc.Channels, func(a, b *Channel) int {
		return pos(a) - pos(b)
	})
}

// bufferKind classifies a buffer name into channel vs query.
func bufferKind(name string) ChannelKind {
	if name == StatusBuffer {
		return KindStatus
	}
	if isChannelName(name) {
		return KindChannel
	}
	return KindQuery
}

// StatusBuffer is the per-network status/server buffer name.
const StatusBuffer = "*status"

// IsQueryBuffer reports whether a buffer name is a private query (a DM): not a
// channel and not the per-network status buffer. The server uses this to treat
// incoming DMs as notification-worthy even when they match no highlight rule.
func IsQueryBuffer(name string) bool {
	return name != StatusBuffer && !isChannelName(name)
}

// isChannelName reports whether name looks like an IRC channel (vs a query).
func isChannelName(name string) bool {
	if name == "" {
		return false
	}
	switch name[0] {
	case '#', '&', '+', '!':
		return true
	default:
		return false
	}
}

func lower(s string) string { return toLowerASCII(s) }

// membershipPrefixOrder ranks channel prefix symbols highest-first
// (owner, admin, op, half-op, voice) — matching the client's nicklist sort.
const membershipPrefixOrder = "~&@%+"

// addPrefix inserts a membership prefix symbol into a member's mode string,
// keeping symbols ordered highest-first and avoiding duplicates, so the
// client reads Modes[0] as the effective rank.
func addPrefix(modes, sym string) string {
	if sym == "" || strings.Contains(modes, sym) {
		return modes
	}
	rank := func(b byte) int {
		if i := strings.IndexByte(membershipPrefixOrder, b); i >= 0 {
			return i
		}
		return len(membershipPrefixOrder)
	}
	sr := rank(sym[0])
	for i := 0; i < len(modes); i++ {
		if rank(modes[i]) > sr {
			return modes[:i] + sym + modes[i:]
		}
	}
	return modes + sym
}

// updateChannelMode merges channel mode changes (e.g. "+ntk key") into the
// existing mode string (e.g. "+n").
func updateChannelMode(current, change string) string {
	changeFlags, _, _ := strings.Cut(strings.TrimSpace(change), " ")
	if !strings.HasPrefix(changeFlags, "+") && !strings.HasPrefix(changeFlags, "-") {
		return changeFlags
	}
	adding := true
	modeSet := map[rune]bool{}
	if strings.HasPrefix(current, "+") {
		for _, r := range current[1:] {
			modeSet[r] = true
		}
	}
	for _, r := range changeFlags {
		if r == '+' {
			adding = true
		} else if r == '-' {
			adding = false
		} else {
			if adding {
				modeSet[r] = true
			} else {
				delete(modeSet, r)
			}
		}
	}
	if len(modeSet) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('+')
	for r := range modeSet {
		b.WriteRune(r)
	}
	return b.String()
}
