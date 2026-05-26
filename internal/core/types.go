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

// Network is one IRC connection's state.
type Network struct {
	ID       string
	Name     string
	Nick     string
	State    ConnState
	Channels []*Channel
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
func (n *Network) getOrCreate(name string, kind ChannelKind) *Channel {
	if c := n.Channel(name); c != nil {
		return c
	}
	c := &Channel{Name: name, Kind: kind, Members: map[string]*Member{}}
	n.Channels = append(n.Channels, c)
	return c
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

// Channel is a chat buffer: a real channel, a private query, or status.
type Channel struct {
	Name      string
	Kind      ChannelKind
	Topic     string
	Members   map[string]*Member
	Unread    int
	Highlight int
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
	ID      string
	Network string
	Buffer  string // channel or query name
	Time    time.Time
	From    string
	Account string
	Kind    MsgKind
	Text    string
	Tags    map[string]string
	Self    bool // echo-message: we sent this
}
