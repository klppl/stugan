package server

import (
	"strings"
	"time"

	"github.com/klippelism/stugan/internal/core"
	"github.com/klippelism/stugan/internal/proto"
)

// applyUnread seeds each channel's unread/highlight badge counts in an init
// snapshot from the persisted per-buffer read markers. Matching is
// case-insensitive on the buffer name so a marker recorded under one casing
// still lights up the channel as the snapshot names it.
func applyUnread(state *proto.InitState, counts []core.UnreadCount) {
	if len(counts) == 0 {
		return
	}
	type key struct{ net, buf string }
	byBuf := make(map[key]core.UnreadCount, len(counts))
	for _, u := range counts {
		byBuf[key{u.Network, strings.ToLower(u.Buffer)}] = u
	}
	for ni := range state.Networks {
		n := &state.Networks[ni]
		for ci := range n.Channels {
			c := &n.Channels[ci]
			if u, ok := byBuf[key{n.ID, strings.ToLower(c.Name)}]; ok {
				c.Unread = u.Unread
				c.Highlight = u.Highlight
			}
		}
	}
}

// toMessageDTO projects a core.Message onto its wire form.
func toMessageDTO(m core.Message) proto.MessageDTO {
	t := ""
	if !m.Time.IsZero() {
		t = m.Time.UTC().Format(time.RFC3339)
	}
	return proto.MessageDTO{
		ID:        m.ID,
		Network:   m.Network,
		Buffer:    m.Buffer,
		Time:      t,
		From:      m.From,
		Kind:      string(m.Kind),
		Text:      m.Text,
		Self:      m.Self,
		Highlight: m.Highlight,
		Tags:      m.Tags,
	}
}

// toMessageDTOs projects a slice of messages.
func toMessageDTOs(ms []core.Message) []proto.MessageDTO {
	out := make([]proto.MessageDTO, len(ms))
	for i, m := range ms {
		out[i] = toMessageDTO(m)
	}
	return out
}

// netAddParams converts a net:add request into runtime params; the network's
// name doubles as its id.
func netAddParams(d proto.NetAdd) core.NetworkParams {
	return core.NetworkParams{
		ID: d.Name, Name: d.Name, Addr: d.Addr, TLS: d.TLS, Insecure: d.Insecure,
		Nick: d.Nick, User: d.User, Realname: d.Realname,
		SASLUser: d.SASLUser, SASLPass: d.SASLPass, Channels: d.Channels,
		ServerPass: d.ServerPass, Perform: d.Perform,
		SASLExternal: d.SASLExternal, CertPEM: d.CertPEM,
	}
}

// netConfigParams converts a net:edit/net:info config into runtime params; the
// Network field identifies the existing network being edited.
func netConfigParams(d proto.NetConfig) core.NetworkParams {
	return core.NetworkParams{
		ID: d.Network, Name: d.Network, Addr: d.Addr, TLS: d.TLS, Insecure: d.Insecure,
		Nick: d.Nick, User: d.User, Realname: d.Realname,
		SASLUser: d.SASLUser, SASLPass: d.SASLPass, Channels: d.Channels,
		ServerPass: d.ServerPass, Perform: d.Perform,
		SASLExternal: d.SASLExternal, CertPEM: d.CertPEM,
	}
}

// netConfigDTO projects runtime params onto the editable NetConfig wire form
// (the net:info reply).
func netConfigDTO(p core.NetworkParams) proto.NetConfig {
	return proto.NetConfig{
		Network: p.ID, Name: p.Name, Addr: p.Addr, TLS: p.TLS, Insecure: p.Insecure,
		Nick: p.Nick, User: p.User, Realname: p.Realname,
		SASLUser: p.SASLUser, SASLPass: p.SASLPass, Channels: p.Channels,
		ServerPass: p.ServerPass, Perform: p.Perform,
		SASLExternal: p.SASLExternal, CertPEM: p.CertPEM,
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
		Caps:     n.Caps,
		Channels: chans,
	}
}

// toPluginInfos projects the engine's plugin list onto its wire form.
func toPluginInfos(ps []core.PluginInfo) []proto.PluginInfo {
	out := make([]proto.PluginInfo, len(ps))
	for i, p := range ps {
		out[i] = proto.PluginInfo{
			Name:        p.Name,
			Description: p.Description,
			Loaded:      p.Loaded,
			Disabled:    p.Disabled,
			Errors:      p.Errors,
			Commands:    p.Commands,
			Hooks:       p.Hooks,
			Settings:    toPluginSettings(p.Settings),
		}
	}
	return out
}

// toPluginSettings projects a plugin's declared settings. The host already
// blanks Value for secret settings; we never copy a secret value here either.
func toPluginSettings(ss []core.PluginSetting) []proto.PluginSetting {
	if len(ss) == 0 {
		return nil
	}
	out := make([]proto.PluginSetting, len(ss))
	for i, s := range ss {
		val := s.Value
		if s.Secret {
			val = ""
		}
		out[i] = proto.PluginSetting{
			Name:    s.Name,
			Type:    s.Type,
			Label:   s.Label,
			Help:    s.Help,
			Value:   val,
			Default: s.Default,
			Secret:  s.Secret,
			Options: s.Options,
		}
	}
	return out
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
		State:     c.State,
	}
}
