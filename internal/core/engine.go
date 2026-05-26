// Package core is the GUI- and transport-independent brain of stugan: the
// domain types, the connection state machine, and the event bus through
// which every meaningful event flows. Plugin hooks fire on the bus and may
// drop or mutate mutable events before they are committed.
package core

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// NetworkSpec seeds a network's initial state when it is registered.
type NetworkSpec struct {
	ID   string
	Name string
	Nick string
}

// Sink receives committed buffer lines (post-hook, post-state-update). In
// Phase 1 the default sink prints to a terminal; later the server bridges
// these to WebSocket clients and the store persists them.
type Sink interface {
	Print(m Message)
}

// Options configures a new Engine.
type Options struct {
	Logger *slog.Logger
	Host   PluginHost // nil → events pass through unchanged
	Sink   Sink       // nil → a logger-backed sink
	User   *User      // nil → a single implicit user
}

// Engine owns the domain state and serializes all mutation onto a single
// loop goroutine fed by the event bus. It implements ConnHandler.
type Engine struct {
	log  *slog.Logger
	host PluginHost
	sink Sink
	user *User

	conns map[string]IRCConn

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
	sink := opts.Sink
	if sink == nil {
		sink = logSink{log}
	}
	user := opts.User
	if user == nil {
		user = &User{ID: "default", Name: "default"}
	}
	return &Engine{
		log:    log,
		host:   host,
		sink:   sink,
		user:   user,
		conns:  map[string]IRCConn{},
		events: make(chan Event, 256),
		done:   make(chan struct{}),
	}
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

// handle runs plugin hooks for mutable events, then applies the result.
func (e *Engine) handle(ctx context.Context, ev Event) {
	if ev.mutable() {
		out, keep := e.host.Dispatch(ctx, ev)
		if !keep {
			return // a hook dropped it
		}
		ev = out
	}
	e.apply(ev)
}

// apply commits an event to state and emits any resulting buffer line.
func (e *Engine) apply(ev Event) {
	n := e.user.Network(ev.Network)
	if n == nil {
		return
	}
	switch ev.Type {
	case evSetState:
		n.State = ev.State

	case EvConnect:
		n.State = StateRegistered
		if ev.Nick != "" {
			n.Nick = ev.Nick
		}
		e.system(n, StatusBuffer, fmt.Sprintf("connected to %s as %s", n.Name, n.Nick))

	case EvDisconnect:
		n.State = StateDisconnected
		msg := "disconnected"
		if ev.Text != "" {
			msg += ": " + ev.Text
		}
		e.system(n, StatusBuffer, msg)

	case EvMessageIn, EvMessageOut:
		if ev.Message == nil {
			return
		}
		m := *ev.Message
		kind := KindChannel
		if !isChannelName(m.Buffer) {
			kind = KindQuery
		}
		n.getOrCreate(m.Buffer, kind)
		e.sink.Print(m)

	case EvJoin:
		c := n.getOrCreate(ev.Channel, KindChannel)
		c.Members[lower(ev.Nick)] = &Member{Nick: ev.Nick, Account: ev.Account}
		e.system(n, ev.Channel, fmt.Sprintf("%s has joined %s", ev.Nick, ev.Channel))

	case EvPart:
		if c := n.Channel(ev.Channel); c != nil {
			delete(c.Members, lower(ev.Nick))
		}
		line := fmt.Sprintf("%s has left %s", ev.Nick, ev.Channel)
		if ev.Text != "" {
			line += " (" + ev.Text + ")"
		}
		e.system(n, ev.Channel, line)

	case EvQuit:
		line := ev.Nick + " has quit"
		if ev.Text != "" {
			line += " (" + ev.Text + ")"
		}
		for _, c := range n.Channels {
			if _, ok := c.Members[lower(ev.Nick)]; ok {
				delete(c.Members, lower(ev.Nick))
				e.system(n, c.Name, line)
			}
		}

	case EvNick:
		if eqFold(n.Nick, ev.Nick) {
			n.Nick = ev.NewNick
		}
		for _, c := range n.Channels {
			if mem, ok := c.Members[lower(ev.Nick)]; ok {
				delete(c.Members, lower(ev.Nick))
				mem.Nick = ev.NewNick
				c.Members[lower(ev.NewNick)] = mem
				e.system(n, c.Name, fmt.Sprintf("%s is now known as %s", ev.Nick, ev.NewNick))
			}
		}

	case EvTopic:
		c := n.getOrCreate(ev.Channel, KindChannel)
		c.Topic = ev.Text
		who := ev.Nick
		if who == "" {
			who = "topic"
		}
		e.system(n, ev.Channel, fmt.Sprintf("%s set topic: %s", who, ev.Text))
	}
}

// system emits a synthetic system line into a buffer.
func (e *Engine) system(n *Network, buffer, text string) {
	e.sink.Print(Message{
		Network: n.ID,
		Buffer:  buffer,
		Time:    time.Now(),
		Kind:    MsgSystem,
		Text:    text,
	})
}

// User exposes the engine's user state (read-only use by server/plugins).
func (e *Engine) User() *User { return e.user }

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
