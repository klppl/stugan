// Package core is the GUI- and transport-independent brain of stugan: the
// domain types, the connection state machine, and the event bus through
// which every meaningful event flows. Plugin hooks fire on the bus and may
// drop or mutate mutable events before they are committed.
package core

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// NetworkSpec seeds a network's initial state when it is registered.
type NetworkSpec struct {
	ID   string
	Name string
	Nick string
}

// Sink observes committed state changes (post-hook, post-state-update),
// the read side of the bus. Print receives each new buffer line;
// NetworkChanged receives a snapshot of a network whose structure changed
// (connection state, nick, buffers, members, topic). Implementations are
// called synchronously from the engine loop and must not block: marshal or
// enqueue and return.
type Sink interface {
	Print(m Message)
	NetworkChanged(n *Network)
}

// Options configures a new Engine.
type Options struct {
	Logger    *slog.Logger
	Host      PluginHost        // nil → events pass through unchanged
	Sink      Sink              // nil → a logger-backed sink
	User      *User             // nil → a single implicit user
	Highlight *Highlighter      // nil → only nick mentions highlight (via a default)
	Aliases   map[string]string // command aliases with $1/$2/$* substitution
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
	conns     map[string]IRCConn

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
		log:       log,
		host:      host,
		sinks:     sinks,
		user:      user,
		highlight: hl,
		aliases:   opts.Aliases,
		conns:     map[string]IRCConn{},
		events:    make(chan Event, 256),
		done:      make(chan struct{}),
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

// AddNetwork registers a network's initial state and its connection. Call
// before Run.
func (e *Engine) AddNetwork(spec NetworkSpec, conn IRCConn) {
	e.user.Networks = append(e.user.Networks, &Network{
		ID:    spec.ID,
		Name:  spec.Name,
		Nick:  spec.Nick,
		State: StateDisconnected,
	})
	e.conns[spec.ID] = conn
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
func (e *Engine) Run(ctx context.Context) error {
	var wg sync.WaitGroup

	wg.Go(func() { e.loop(ctx) })

	for id, conn := range e.conns {
		wg.Go(func() { e.runConn(ctx, id, conn) })
	}

	<-ctx.Done()
	e.closeo.Do(func() { close(e.done) })
	for id, conn := range e.conns {
		if err := conn.Close(); err != nil {
			e.log.Warn("closing connection", "network", id, "err", err)
		}
	}
	wg.Wait()
	if err := e.host.Close(); err != nil {
		e.log.Warn("closing plugin host", "err", err)
	}
	return nil
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
	}
}

// outbound describes an IRC send to perform after state mutation, outside
// the state lock.
type outbound struct {
	target string
	text   string
}

// apply commits an event: it mutates state under the write lock, then
// performs I/O (sink fan-out, conn send) with the lock released.
func (e *Engine) apply(ev Event) {
	e.mu.Lock()
	emit, send, netChanged := e.applyLocked(ev)
	e.mu.Unlock()

	for _, m := range emit {
		e.broadcast(m)
	}
	if netChanged {
		e.notifyNetwork(ev.Network)
	}
	if send != nil {
		if conn := e.conns[ev.Network]; conn != nil {
			if err := conn.Message(send.target, send.text); err != nil {
				e.log.Warn("send failed", "network", ev.Network, "err", err)
			}
		}
	}
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

// applyLocked mutates state for ev and returns the buffer lines to emit, an
// optional IRC send, and whether the network's structure changed (so a
// snapshot should be pushed to observers). The caller holds e.mu for writing.
func (e *Engine) applyLocked(ev Event) (emit []Message, send *outbound, netChanged bool) {
	n := e.user.Network(ev.Network)
	if n == nil {
		return nil, nil, false
	}
	sys := func(buffer, text string) {
		emit = append(emit, Message{
			Network: n.ID, Buffer: buffer, Time: time.Now(),
			Kind: MsgSystem, Text: text,
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
			return emit, nil, false
		}
		m := *ev.Message
		if !m.Self && (m.Kind == MsgPrivmsg || m.Kind == MsgNotice || m.Kind == MsgAction) {
			m.Highlight = e.highlight.Match(m.Text, n.Nick)
		}
		_, created := n.getOrCreate(m.Buffer, bufferKind(m.Buffer))
		emit = append(emit, m)
		netChanged = created

	case EvMessageOut:
		if ev.Message == nil {
			return emit, nil, false
		}
		m := *ev.Message
		if m.From == "" {
			m.From = n.Nick
		}
		_, created := n.getOrCreate(m.Buffer, bufferKind(m.Buffer))
		emit = append(emit, m)
		send = &outbound{target: m.Buffer, text: m.Text}
		netChanged = created

	case EvJoin:
		c, _ := n.getOrCreate(ev.Channel, KindChannel)
		c.Members[lower(ev.Nick)] = &Member{Nick: ev.Nick, Account: ev.Account}
		sys(ev.Channel, fmt.Sprintf("%s has joined %s", ev.Nick, ev.Channel))
		netChanged = true

	case EvPart:
		// If we are the one parting, drop the buffer entirely; otherwise just
		// remove the member.
		if eqFold(ev.Nick, n.Nick) {
			n.remove(ev.Channel)
		} else if c := n.Channel(ev.Channel); c != nil {
			delete(c.Members, lower(ev.Nick))
		}
		line := fmt.Sprintf("%s has left %s", ev.Nick, ev.Channel)
		if ev.Text != "" {
			line += " (" + ev.Text + ")"
		}
		sys(ev.Channel, line)
		netChanged = true

	case EvQuit:
		line := ev.Nick + " has quit"
		if ev.Text != "" {
			line += " (" + ev.Text + ")"
		}
		for _, c := range n.Channels {
			if _, ok := c.Members[lower(ev.Nick)]; ok {
				delete(c.Members, lower(ev.Nick))
				sys(c.Name, line)
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
				sys(c.Name, fmt.Sprintf("%s is now known as %s", ev.Nick, ev.NewNick))
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
	return emit, send, netChanged
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
