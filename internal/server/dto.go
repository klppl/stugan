package server

import (
	"time"

	"github.com/klippelism/stugan/internal/core"
	"github.com/klippelism/stugan/internal/proto"
)

// toMessageDTO projects a core.Message onto its wire form.
func toMessageDTO(m core.Message) proto.MessageDTO {
	t := ""
	if !m.Time.IsZero() {
		t = m.Time.UTC().Format(time.RFC3339)
	}
	return proto.MessageDTO{
		ID:      m.ID,
		Network: m.Network,
		Buffer:  m.Buffer,
		Time:    t,
		From:    m.From,
		Kind:    string(m.Kind),
		Text:    m.Text,
		Self:    m.Self,
		Tags:    m.Tags,
	}
}

// toInitState projects a user-state snapshot onto the init payload.
func toInitState(u *core.User) proto.InitState {
	nets := make([]proto.NetworkDTO, 0, len(u.Networks))
	for _, n := range u.Networks {
		nets = append(nets, toNetworkDTO(n))
	}
	return proto.InitState{
		User:     proto.UserDTO{ID: u.ID, Name: u.Name},
		Networks: nets,
	}
}

func toNetworkDTO(n *core.Network) proto.NetworkDTO {
	chans := make([]proto.ChannelDTO, 0, len(n.Channels))
	for _, c := range n.Channels {
		chans = append(chans, toChannelDTO(c))
	}
	return proto.NetworkDTO{
		ID:       n.ID,
		Name:     n.Name,
		Nick:     n.Nick,
		State:    string(n.State),
		Channels: chans,
	}
}

func toChannelDTO(c *core.Channel) proto.ChannelDTO {
	mems := make([]proto.MemberDTO, 0, len(c.Members))
	for _, m := range c.Members {
		mems = append(mems, proto.MemberDTO{Nick: m.Nick, Modes: m.Modes, Away: m.Away})
	}
	return proto.ChannelDTO{
		Name:      c.Name,
		Kind:      string(c.Kind),
		Topic:     c.Topic,
		Members:   mems,
		Unread:    c.Unread,
		Highlight: c.Highlight,
	}
}
