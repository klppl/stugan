package irc

import (
	"testing"

	"github.com/klippelism/stugan/internal/core"
	"github.com/lrstanley/girc"
)

func TestToEvent(t *testing.T) {
	const self = "me"
	tests := []struct {
		name string
		raw  string
		ok   bool
		want core.Event // only the fields we assert on are set
		text string     // expected Message.Text for message events
	}{
		{
			name: "channel privmsg",
			raw:  ":alice!u@h PRIVMSG #go :hello there",
			ok:   true,
			want: core.Event{Type: core.EvMessageIn, Network: "n"},
			text: "hello there",
		},
		{
			name: "direct privmsg routes to sender query",
			raw:  ":alice!u@h PRIVMSG me :psst",
			ok:   true,
			want: core.Event{Type: core.EvMessageIn, Network: "n"},
			text: "psst",
		},
		{
			name: "notice",
			raw:  ":serv NOTICE #go :heads up",
			ok:   true,
			want: core.Event{Type: core.EvMessageIn, Network: "n"},
			text: "heads up",
		},
		{
			name: "action",
			raw:  ":alice!u@h PRIVMSG #go :\x01ACTION waves\x01",
			ok:   true,
			want: core.Event{Type: core.EvMessageIn, Network: "n"},
			text: "waves",
		},
		{
			name: "join",
			raw:  ":bob!u@h JOIN #go",
			ok:   true,
			want: core.Event{Type: core.EvJoin, Network: "n", Nick: "bob", Channel: "#go"},
		},
		{
			// extended-join: [channel, account, realname] — channel must be
			// the first param, not the trailing realname.
			name: "extended join",
			raw:  ":bob!u@h JOIN #go bobacct :Bob Real",
			ok:   true,
			want: core.Event{Type: core.EvJoin, Network: "n", Nick: "bob", Channel: "#go", Account: "bobacct"},
		},
		{
			name: "part with reason",
			raw:  ":bob!u@h PART #go :bye",
			ok:   true,
			want: core.Event{Type: core.EvPart, Network: "n", Nick: "bob", Channel: "#go", Text: "bye"},
		},
		{
			name: "quit",
			raw:  ":bob!u@h QUIT :ping timeout",
			ok:   true,
			want: core.Event{Type: core.EvQuit, Network: "n", Nick: "bob", Text: "ping timeout"},
		},
		{
			name: "nick",
			raw:  ":bob!u@h NICK :bobby",
			ok:   true,
			want: core.Event{Type: core.EvNick, Network: "n", Nick: "bob", NewNick: "bobby"},
		},
		{
			name: "topic",
			raw:  ":op!u@h TOPIC #go :the new topic",
			ok:   true,
			want: core.Event{Type: core.EvTopic, Network: "n", Channel: "#go", Text: "the new topic", Nick: "op"},
		},
		{
			name: "unmodeled numeric ignored",
			raw:  ":serv 001 me :Welcome",
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := girc.ParseEvent(tt.raw)
			if e == nil {
				t.Fatalf("ParseEvent(%q) returned nil", tt.raw)
			}
			got, ok := toEvent("n", e, self)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !tt.ok {
				return
			}
			if got.Type != tt.want.Type {
				t.Errorf("Type = %q, want %q", got.Type, tt.want.Type)
			}
			if got.Nick != tt.want.Nick {
				t.Errorf("Nick = %q, want %q", got.Nick, tt.want.Nick)
			}
			if got.NewNick != tt.want.NewNick {
				t.Errorf("NewNick = %q, want %q", got.NewNick, tt.want.NewNick)
			}
			if got.Channel != tt.want.Channel {
				t.Errorf("Channel = %q, want %q", got.Channel, tt.want.Channel)
			}
			if got.Account != tt.want.Account {
				t.Errorf("Account = %q, want %q", got.Account, tt.want.Account)
			}
			if tt.want.Text != "" && got.Text != tt.want.Text {
				t.Errorf("Text = %q, want %q", got.Text, tt.want.Text)
			}
			if tt.text != "" {
				if got.Message == nil {
					t.Fatalf("Message is nil")
				}
				if got.Message.Text != tt.text {
					t.Errorf("Message.Text = %q, want %q", got.Message.Text, tt.text)
				}
			}
		})
	}
}

// Buffer routing depends on whether a message is inbound or our own echo.
func TestToEventQueryBufferRouting(t *testing.T) {
	const self = "me"

	// Inbound DM from alice → query keyed by alice.
	in := girc.ParseEvent(":alice!u@h PRIVMSG me :hi")
	ev, ok := toEvent("n", in, self)
	if !ok || ev.Message.Buffer != "alice" {
		t.Fatalf("inbound DM buffer = %q, want alice", ev.Message.Buffer)
	}

	// Our own echo'd DM to alice → query keyed by alice (the target), and
	// classified as outbound.
	out := girc.ParseEvent("@batch=1 :me!u@h PRIVMSG alice :hi back")
	out.Echo = true
	ev, ok = toEvent("n", out, self)
	if !ok {
		t.Fatal("echo event not ok")
	}
	if ev.Type != core.EvMessageOut {
		t.Errorf("Type = %q, want message_out", ev.Type)
	}
	if ev.Message.Buffer != "alice" {
		t.Errorf("echo DM buffer = %q, want alice", ev.Message.Buffer)
	}
	if !ev.Message.Self {
		t.Error("echo message Self = false, want true")
	}
}

func TestToEventNames(t *testing.T) {
	// 353 with multi-prefix and userhost-in-names entries.
	e := girc.ParseEvent(":serv 353 me = #go :alice @bob +carol @+dave!u@h ~owner")
	if e == nil {
		t.Fatal("ParseEvent nil")
	}
	ev, ok := toEvent("n", e, "me")
	if !ok || ev.Type != core.EvNames {
		t.Fatalf("type = %q ok=%v, want names", ev.Type, ok)
	}
	if ev.Channel != "#go" {
		t.Errorf("channel = %q", ev.Channel)
	}
	want := map[string]string{"alice": "", "bob": "@", "carol": "+", "dave": "@+", "owner": "~"}
	if len(ev.Members) != len(want) {
		t.Fatalf("got %d members, want %d: %+v", len(ev.Members), len(want), ev.Members)
	}
	for _, m := range ev.Members {
		if want[m.Nick] != m.Modes {
			t.Errorf("member %q modes = %q, want %q", m.Nick, m.Modes, want[m.Nick])
		}
	}
}

// A NOTICE from the server itself (no user/host in the source) routes to
// the status buffer rather than creating a query named after the server.
func TestServerNoticeRoutesToStatus(t *testing.T) {
	e := girc.ParseEvent(":uranium.libera.chat NOTICE * :*** Checking Ident")
	ev, ok := toEvent("n", e, "me")
	if !ok {
		t.Fatal("server notice not ok")
	}
	if ev.Message.Buffer != core.StatusBuffer {
		t.Errorf("buffer = %q, want %q", ev.Message.Buffer, core.StatusBuffer)
	}
}

func TestSplitAddr(t *testing.T) {
	tests := []struct {
		addr     string
		tls      bool
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{"irc.libera.chat:6697", true, "irc.libera.chat", 6697, false},
		{"irc.example.com", true, "irc.example.com", 6697, false},
		{"irc.example.com", false, "irc.example.com", 6667, false},
		{"host:notaport", false, "", 0, true},
		{"", false, "", 0, true},
	}
	for _, tt := range tests {
		host, port, err := splitAddr(tt.addr, tt.tls)
		if (err != nil) != tt.wantErr {
			t.Errorf("splitAddr(%q): err = %v, wantErr %v", tt.addr, err, tt.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if host != tt.wantHost || port != tt.wantPort {
			t.Errorf("splitAddr(%q) = %q,%d; want %q,%d", tt.addr, host, port, tt.wantHost, tt.wantPort)
		}
	}
}
