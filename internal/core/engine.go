// Package core is the GUI- and transport-independent brain of stugan: the
// domain types, the connection state machine, and the event bus through
// which every meaningful event flows. Plugin hooks fire on the bus and may
// drop or mutate mutable events before they are committed.
package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
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
	// ChannelList delivers the result of a channel-browser LIST request.
	ChannelList(network string, items []ChannelListItem)
	// Typing delivers an inbound typing notification (state is
	// active/paused/done).
	Typing(network, buffer, nick, state string)
}

// ChannelListItem is one entry in a LIST (channel-browser) result.
type ChannelListItem struct {
	Name  string
	Users int
	Topic string
}

// maxListItems caps a LIST result so a huge network can't exhaust memory or
// produce an enormous frame.
const maxListItems = 2000

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
}

// Engine owns the domain state and serializes all mutation onto a single
// loop goroutine fed by the event bus. It implements ConnHandler.
//
// State (the user/networks/channels tree) is mutated only by the loop, but
// it is also read concurrently by server goroutines (snapshots), so it is
// guarded by mu: the loop write-locks for the brief mutation, readers
// read-lock. I/O (sink fan-out, conn sends) happens outside the lock.
type Engine struct {
	log   *slog.Logger
	host  PluginHost
	sinks []Sink

	mu   sync.RWMutex
	user *User

	highlight *Highlighter
	aliases   map[string]string
	connector Connector
	netStore  NetworkStore

	// conns and the run-state below are guarded by mu.
	conns       map[string]IRCConn
	connCancels map[string]context.CancelFunc
	listAccum   map[string][]ChannelListItem // in-progress LIST results
	// pendingWhois records which buffer issued a WHOIS/WHOWAS/WHO so we
	// can route the server's numeric replies back to it. Key is
	// "<network>\t<lowercase-nick>"; cleared on the matching end-of
	// marker (318/369/315). Mutated only on the engine loop goroutine.
	pendingWhois map[string]string
	running      bool
	runCtx       context.Context
	runWG        sync.WaitGroup

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
		user:         user,
		highlight:    hl,
		aliases:      opts.Aliases,
		connector:    opts.Connector,
		netStore:     opts.Networks,
		conns:        map[string]IRCConn{},
		connCancels:  map[string]context.CancelFunc{},
		listAccum:    map[string][]ChannelListItem{},
		pendingWhois: map[string]string{},
		events:       make(chan Event, 256),
		done:         make(chan struct{}),
	}
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

// AddNetwork registers a pre-built connection and its initial state. Call
// before Run (used for networks loaded at startup).
func (e *Engine) AddNetwork(p NetworkParams, conn IRCConn) {
	e.mu.Lock()
	e.user.Networks = append(e.user.Networks, &Network{
		ID: p.ID, Name: p.Name, Nick: p.Nick, State: StateDisconnected, Params: p,
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
		ID: p.ID, Name: p.Name, Nick: p.Nick, State: StateDisconnected, Params: p,
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
		n.Params, n.Name, n.Nick = p, p.Name, p.Nick
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
	n.Params, n.Name = p, p.Name
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

// NetworkConfig returns the stored connection params for a network.
func (e *Engine) NetworkConfig(id string) (NetworkParams, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if n := e.user.Network(id); n != nil {
		return n.Params, true
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
			// against alias loops).
			if tmpl, ok := e.aliases[strings.ToLower(name)]; ok && depth < 8 {
				e.sendInput(network, buffer, expandAlias(tmpl, strings.Fields(argstr)), depth+1)
				return
			}
			e.HandleEvent(Event{
				Type: EvCommand, Network: network, Channel: buffer, Time: time.Now(),
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
	return e.user.clone()
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
func (e *Engine) runConn(ctx context.Context, id string, conn IRCConn) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		e.HandleEvent(Event{Type: evSetState, Network: id, State: StateConnecting})
		err := conn.Connect(ctx)
		if ctx.Err() != nil {
			return
		}
		e.log.Warn("connection ended; will retry", "network", id, "err", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
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
	case evSetState, evPrint:
		e.apply(ev)

	case EvMessageIn, EvMessageOut:
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

	default: // join, part, quit, nick, topic, connect, disconnect
		e.host.Dispatch(ctx, ev) // notify-only
		e.apply(ev)
		if ev.Type == EvConnect {
			e.runPerform(ev.Network)
		}
	}
}

// runPerform replays a network's configured perform commands after it
// registers, on every (re)connect. Lines are submitted through the normal
// input path (alias expansion, /command dispatch, plugin hooks), so they
// behave exactly as if the user had typed them in the status buffer. It runs
// in its own goroutine: sendInput enqueues onto the event bus, and we are
// already on the loop goroutine reading that bus, so submitting inline could
// stall if the buffer filled.
func (e *Engine) runPerform(network string) {
	e.mu.RLock()
	n := e.user.Network(network)
	var lines []string
	if n != nil && len(n.Params.Perform) > 0 {
		lines = append(lines, n.Params.Perform...)
	}
	e.mu.RUnlock()
	if len(lines) == 0 {
		return
	}
	go func() {
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			e.SendInput(network, StatusBuffer, line)
		}
	}()
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
				Name: ev.Channel, Users: ev.Count, Topic: ev.Text,
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
			s.Typing(ev.Network, ev.Channel, ev.Nick, ev.Text)
		}
		return
	case EvNumeric:
		e.applyNumeric(ev)
		return
	}

	e.mu.Lock()
	emit, netChanged := e.applyLocked(ev)
	e.mu.Unlock()

	for _, m := range emit {
		e.broadcast(m)
	}
	if netChanged {
		e.notifyNetwork(ev.Network)
	}
}

// applyNumeric routes a server numeric (WHOIS/WHO/WHOWAS reply, an error
// code, …) into the buffer that issued the request when we can pair them,
// else into the per-network status buffer. pendingWhois is keyed by
// "<network>\t<lowercase-nick>" and cleared on the matching end-of marker
// so a long-lived plugin doesn't leak entries.
func (e *Engine) applyNumeric(ev Event) {
	key := ev.Network + "\t" + strings.ToLower(ev.Nick)
	buf := StatusBuffer
	if b, ok := e.pendingWhois[key]; ok && b != "" {
		buf = b
	}
	switch ev.Count {
	case 318, 369, 315: // RPL_ENDOFWHOIS / WHOWAS / WHO
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
	conn := e.connFor(network)
	if conn == nil || !slices.Contains(conn.Caps(), "message-tags") {
		return
	}
	if state != "active" && state != "paused" && state != "done" {
		return
	}
	_ = conn.SendRaw("@+typing=" + state + " TAGMSG " + buffer)
}

// echoMessage reports whether the network's connection negotiated the
// echo-message capability (so the server will echo our sent lines back).
func (e *Engine) echoMessage(network string) bool {
	conn := e.connFor(network)
	if conn == nil {
		return false
	}
	return slices.Contains(conn.Caps(), "echo-message")
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

// applyLocked mutates state for ev and returns the buffer lines to emit and
// whether the network's structure changed (so a snapshot should be pushed to
// observers). The caller holds e.mu for writing. EvMessageOut is handled
// separately (see applyMessageOut) because it needs the echo-message
// capability check, which can't take the lock that this holds.
func (e *Engine) applyLocked(ev Event) (emit []Message, netChanged bool) {
	n := e.user.Network(ev.Network)
	if n == nil {
		return nil, false
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

	case evPrint:
		_, created := n.getOrCreate(ev.Channel, bufferKind(ev.Channel))
		emit = append(emit, Message{
			Network: n.ID, Buffer: ev.Channel, Time: time.Now(),
			Kind: MsgSystem, Text: ev.Text,
		})
		netChanged = created

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
		netChanged = true

	case EvMessageIn:
		if ev.Message == nil {
			return emit, false
		}
		m := *ev.Message
		if !m.Self && (m.Kind == MsgPrivmsg || m.Kind == MsgNotice || m.Kind == MsgAction) {
			m.Highlight = e.highlight.Match(m.Text, n.Nick)
		}
		_, created := n.getOrCreate(m.Buffer, bufferKind(m.Buffer))
		emit = append(emit, m)
		netChanged = created

	case EvNames:
		// The server's NAMES reply on join: merge the listed members
		// without emitting join lines.
		c, _ := n.getOrCreate(ev.Channel, KindChannel)
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

	case EvJoin:
		c, _ := n.getOrCreate(ev.Channel, KindChannel)
		c.Members[lower(ev.Nick)] = &Member{Nick: ev.Nick, Account: ev.Account}
		line(MsgJoin, ev.Channel, ev.Nick, fmt.Sprintf("%s has joined %s", ev.Nick, ev.Channel))
		netChanged = true

	case EvPart:
		// If we are the one parting, drop the buffer entirely; otherwise just
		// remove the member.
		if eqFold(ev.Nick, n.Nick) {
			n.remove(ev.Channel)
		} else if c := n.Channel(ev.Channel); c != nil {
			delete(c.Members, lower(ev.Nick))
		}
		text := fmt.Sprintf("%s has left %s", ev.Nick, ev.Channel)
		if ev.Text != "" {
			text += " (" + ev.Text + ")"
		}
		line(MsgPart, ev.Channel, ev.Nick, text)
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

	case EvTopic:
		c, _ := n.getOrCreate(ev.Channel, KindChannel)
		c.Topic = ev.Text
		who := ev.Nick
		if who == "" {
			who = "topic"
		}
		sys(ev.Channel, fmt.Sprintf("%s set topic: %s", who, ev.Text))
		netChanged = true
	}
	return emit, netChanged
}

// broadcast fans a committed line out to every registered sink.
func (e *Engine) broadcast(m Message) {
	for _, s := range e.sinks {
		s.Print(m)
	}
}

// SnapshotNetwork returns a deep copy of one network's state, or nil if it
// is unknown. Safe to call from any goroutine.
func (e *Engine) SnapshotNetwork(id string) *Network {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if n := e.user.Network(id); n != nil {
		return n.clone()
	}
	return nil
}

// bufferKind classifies a buffer name into channel vs query.
func bufferKind(name string) ChannelKind {
	if isChannelName(name) {
		return KindChannel
	}
	return KindQuery
}

// StatusBuffer is the per-network status/server buffer name.
const StatusBuffer = "*status"

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
