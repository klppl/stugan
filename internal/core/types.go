package core

import "time"

// ConnState is a network's connection lifecycle state.
type ConnState string

const (
	StateDisconnected ConnState = "disconnected"
	StateConnecting   ConnState = "connecting"
	StateRegistered   ConnState = "registered"
)

// ChannelKind distinguishes a real channel from a private query and the
// per-network status buffer.
type ChannelKind string

const (
	KindChannel ChannelKind = "channel"
	KindQuery   ChannelKind = "query"
	KindStatus  ChannelKind = "status"
)

// MsgKind classifies a buffer line.
type MsgKind string

const (
	MsgPrivmsg MsgKind = "privmsg"
	MsgNotice  MsgKind = "notice"
	MsgAction  MsgKind = "action"
	MsgJoin    MsgKind = "join"
	MsgPart    MsgKind = "part"
	MsgQuit    MsgKind = "quit"
	MsgNick    MsgKind = "nick"
	MsgTopic   MsgKind = "topic"
	MsgSystem  MsgKind = "system"
)

// User owns networks. Single-user today (one implicit user); multi-user
// later adds a manager and per-user auth without changing this shape.
type User struct {
	ID       string
	Name     string
	Networks []*Network
}

// Network returns the network with the given id, or nil.
func (u *User) Network(id string) *Network {
	for _, n := range u.Networks {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// clone returns a deep copy of the user state, so a reader can traverse it
// without holding the engine lock or racing the mutating loop.
func (u *User) clone() *User {
	c := &User{ID: u.ID, Name: u.Name, Networks: make([]*Network, len(u.Networks))}
	for i, n := range u.Networks {
		c.Networks[i] = n.clone()
	}
	return c
}

// clone returns a deep copy of a network (channels and members included).
func (n *Network) clone() *Network {
	nc := &Network{
		ID: n.ID, Name: n.Name, Nick: n.Nick, State: n.State, Params: n.Params.clone(),
		Channels: make([]*Channel, len(n.Channels)),
	}
	for j, ch := range n.Channels {
		cc := &Channel{
			Name: ch.Name, Kind: ch.Kind, Topic: ch.Topic,
			TopicSetter: ch.TopicSetter, TopicTime: ch.TopicTime, Mode: ch.Mode,
			Unread: ch.Unread, Highlight: ch.Highlight,
			Members: make(map[string]*Member, len(ch.Members)),
		}
		for k, m := range ch.Members {
			mc := *m
			cc.Members[k] = &mc
		}
		if len(ch.State) > 0 {
			cc.State = make(map[string]string, len(ch.State))
			for k, v := range ch.State {
				cc.State[k] = v
			}
		}
		nc.Channels[j] = cc
	}
	if len(n.MonitorOnline) > 0 {
		nc.MonitorOnline = make(map[string]bool, len(n.MonitorOnline))
		for k, v := range n.MonitorOnline {
			nc.MonitorOnline[k] = v
		}
	}
	return nc
}

// Network is one IRC connection's state.
type Network struct {
	ID       string
	Name     string
	Nick     string
	State    ConnState
	Channels []*Channel
	// Caps are the IRCv3 capabilities the live connection negotiated. Set
	// only on snapshots (from the connection), so the client can light up
	// cap-gated affordances (reactions, redaction). Not stored on the live
	// Network; see SnapshotNetwork.
	Caps []string
	// Params is the full connection config (addr, TLS, SASL, …). Retained
	// so the GUI can read/edit it; never included in the wire snapshot.
	Params NetworkParams
	// MonitorOnline is the live presence of monitored nicks (the friends list,
	// Params.Monitor): lowercased nick → online. A monitored nick absent from
	// the map is offline/unknown until the server reports it (730/731). Rebuilt
	// each connection, so not persisted.
	MonitorOnline map[string]bool
}

// Channel returns the buffer with the given name (case-insensitive), or nil.
func (n *Network) Channel(name string) *Channel {
	for _, c := range n.Channels {
		if eqFold(c.Name, name) {
			return c
		}
	}
	return nil
}

// getOrCreate returns the named buffer, creating it with kind if absent.
// created reports whether a new buffer was added.
func (n *Network) getOrCreate(name string, kind ChannelKind) (c *Channel, created bool) {
	if c := n.Channel(name); c != nil {
		return c, false
	}
	c = &Channel{Name: name, Kind: kind, Members: map[string]*Member{}}
	n.Channels = append(n.Channels, c)
	return c, true
}

// remove drops the named buffer if present.
func (n *Network) remove(name string) {
	for i, c := range n.Channels {
		if eqFold(c.Name, name) {
			n.Channels = append(n.Channels[:i], n.Channels[i+1:]...)
			return
		}
	}
}

// addAutojoin records a channel in the persisted auto-join list so it is
// rejoined on the next (re)connect. Only real channels are tracked (queries
// and status are not auto-joinable). When hasKey is set, key is stored as the
// channel's join key (+k password) — an empty key clears any previous one;
// when hasKey is false the stored key (if any) is left untouched, so a server
// re-join echo never wipes a known key. Returns whether anything changed.
func (n *Network) addAutojoin(name, key string, hasKey bool) bool {
	if !isChannelName(name) {
		return false
	}
	changed := true
	for _, c := range n.Params.Channels {
		if eqFold(c, name) {
			changed = false // already present; only a key change can dirty it
			name = c        // keep the stored casing for the key lookup
			break
		}
	}
	if changed {
		n.Params.Channels = append(n.Params.Channels, name)
	}
	if hasKey {
		if n.setChannelKey(name, key) {
			changed = true
		}
	}
	return changed
}

// setChannelKey stores (or, for an empty key, clears) a channel's join key,
// matching the channel case-insensitively. Returns whether the map changed.
func (n *Network) setChannelKey(name, key string) bool {
	cur, had := n.lookupChannelKey(name)
	if key == "" {
		if !had {
			return false
		}
		n.deleteChannelKey(name)
		return true
	}
	if had && cur == key {
		return false
	}
	if n.Params.ChannelKeys == nil {
		n.Params.ChannelKeys = map[string]string{}
	}
	n.deleteChannelKey(name) // drop any differently-cased prior entry
	n.Params.ChannelKeys[name] = key
	return true
}

// lookupChannelKey returns a channel's stored join key (case-insensitive).
func (n *Network) lookupChannelKey(name string) (key string, ok bool) {
	for k, v := range n.Params.ChannelKeys {
		if eqFold(k, name) {
			return v, true
		}
	}
	return "", false
}

// deleteChannelKey removes a channel's join key (case-insensitive).
func (n *Network) deleteChannelKey(name string) {
	for k := range n.Params.ChannelKeys {
		if eqFold(k, name) {
			delete(n.Params.ChannelKeys, k)
		}
	}
}

// removeAutojoin drops a channel (and its join key) from the persisted
// auto-join list. Returns whether anything changed.
func (n *Network) removeAutojoin(name string) bool {
	changed := false
	for i, c := range n.Params.Channels {
		if eqFold(c, name) {
			n.Params.Channels = append(n.Params.Channels[:i], n.Params.Channels[i+1:]...)
			changed = true
			break
		}
	}
	if _, had := n.lookupChannelKey(name); had {
		n.deleteChannelKey(name)
		changed = true
	}
	return changed
}

// Channel is a chat buffer: a real channel, a private query, or status.
//
// State is an opaque per-buffer key/value bag set by plugins via
// API.SetBufferState. Keys are plugin-defined; the engine doesn't interpret
// them. It rides on the snapshot/wire path so clients can react to plugin
// state (e.g. the fish.lua plugin sets {"encrypted": "cbc"} so the sidebar
// can render a lock icon). Nil and empty maps both mean "no state".
type Channel struct {
	Name        string
	Kind        ChannelKind
	Topic       string
	TopicSetter string
	TopicTime   time.Time
	Mode        string
	Members     map[string]*Member
	Unread      int
	Highlight   int
	State       map[string]string
}

// Member is a participant in a channel.
type Member struct {
	Nick    string
	Account string // from account-notify/WHOX; "" if unknown
	Modes   string // channel prefixes, e.g. "@", "+"
	Away    bool
}

// Message is a single line in a buffer. It is the unit plugin hooks
// inspect/mutate and that the wire protocol carries.
type Message struct {
	ID string
	// Seq is the store's monotonic insertion id (SQLite rowid) for a persisted
	// message, used as the stable keyset cursor for history paging. Zero for a
	// live message that hasn't been read back from the store.
	Seq       int64
	Network   string
	Buffer    string // channel or query name
	Time      time.Time
	From      string
	Account   string
	Kind      MsgKind
	Text      string
	Tags      map[string]string
	Self      bool // echo-message: we sent this
	Highlight bool // matched a highlight rule / nick mention
}
