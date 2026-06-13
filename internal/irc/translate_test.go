package irc

import (
	"slices"
	"testing"

	"github.com/klippelism/stugan/internal/core"
	"github.com/lrstanley/girc"
)

// recordHandler captures emitted core events for assertions.
type recordHandler struct{ evs []core.Event }

func (h *recordHandler) HandleEvent(ev core.Event) { h.evs = append(h.evs, ev) }

func TestEmitMonitor(t *testing.T) {
	h := &recordHandler{}
	c, err := New(Options{Network: "n", Addr: "a:6667", Nick: "me"}, h)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// 730: online, nick!user@host comma list — keep just the nicks.
	c.emitMonitor(girc.ParseEvent(":serv 730 me :alice!u@h,bob!u@h"), true)
	// 731: offline, bare nicks.
	c.emitMonitor(girc.ParseEvent(":serv 731 me :carol"), false)
	if len(h.evs) != 2 {
		t.Fatalf("emitted %d events, want 2", len(h.evs))
	}
	if on := h.evs[0]; on.Type != core.EvMonitor || !on.Online ||
		!slices.Equal(on.Args, []string{"alice", "bob"}) {
		t.Fatalf("online event = %+v", on)
	}
	if off := h.evs[1]; off.Online || !slices.Equal(off.Args, []string{"carol"}) {
		t.Fatalf("offline event = %+v", off)
	}
	// An empty target list emits nothing.
	c.emitMonitor(girc.ParseEvent(":serv 731 me :"), false)
	if len(h.evs) != 2 {
		t.Fatal("empty MONITOR list emitted an event")
	}
}

func TestMultilineBatch(t *testing.T) {
	h := &recordHandler{}
	c, err := New(Options{Network: "n", Addr: "a:6667", Nick: "me"}, h)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gc := c.client

	// Open a draft/multiline batch (the BATCH carries the msgid/source), feed
	// three member lines (the third glued on with -concat), then close it.
	c.handleBatch(gc, girc.ParseEvent("@msgid=abc :alice!u@h BATCH +1 draft/multiline #go"))
	for _, raw := range []string{
		"@batch=1 :alice!u@h PRIVMSG #go :line one",
		"@batch=1 :alice!u@h PRIVMSG #go :line two",
		"@batch=1;draft/multiline-concat :alice!u@h PRIVMSG #go : tail",
	} {
		if !c.absorbMultiline(girc.ParseEvent(raw)) {
			t.Fatalf("line not absorbed: %q", raw)
		}
	}
	c.handleBatch(gc, girc.ParseEvent(":alice!u@h BATCH -1"))

	if len(h.evs) != 1 {
		t.Fatalf("emitted %d events, want 1 reassembled message", len(h.evs))
	}
	got := h.evs[0]
	if got.Type != core.EvMessageIn || got.Message == nil {
		t.Fatalf("event = %+v, want one EvMessageIn", got)
	}
	if want := "line one\nline two tail"; got.Message.Text != want {
		t.Fatalf("Text = %q, want %q", got.Message.Text, want)
	}
	if got.Message.Buffer != "#go" || got.Message.From != "alice" || got.Message.ID != "abc" {
		t.Fatalf("routing/meta wrong: buffer=%q from=%q id=%q", got.Message.Buffer, got.Message.From, got.Message.ID)
	}

	// A message tagged for a batch we never opened is not absorbed (it routes
	// normally) — e.g. members of a chathistory batch.
	if c.absorbMultiline(girc.ParseEvent("@batch=99 :bob!u@h PRIVMSG #go :hi")) {
		t.Fatal("absorbed a message for an unopened batch")
	}
}

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
			// A non-ACTION CTCP request is dropped (girc auto-replies); its raw
			// \x01-framed payload must never surface as a buffer message.
			name: "ctcp version request dropped",
			raw:  ":alice!u@h PRIVMSG me :\x01VERSION\x01",
			ok:   false,
		},
		{
			// A CTCP reply (NOTICE) is likewise dropped, not shown as raw text.
			name: "ctcp version reply dropped",
			raw:  ":alice!u@h NOTICE me :\x01VERSION stugan\x01",
			ok:   false,
		},
		{
			name: "join",
			raw:  ":bob!u@h JOIN #go",
			ok:   true,
			want: core.Event{Type: core.EvJoin, Network: "n", Nick: "bob", Buffer: "#go"},
		},
		{
			// extended-join: [channel, account, realname] — channel must be
			// the first param, not the trailing realname.
			name: "extended join",
			raw:  ":bob!u@h JOIN #go bobacct :Bob Real",
			ok:   true,
			want: core.Event{Type: core.EvJoin, Network: "n", Nick: "bob", Buffer: "#go", Account: "bobacct"},
		},
		{
			name: "part with reason",
			raw:  ":bob!u@h PART #go :bye",
			ok:   true,
			want: core.Event{Type: core.EvPart, Network: "n", Nick: "bob", Buffer: "#go", Text: "bye"},
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
			want: core.Event{Type: core.EvTopic, Network: "n", Buffer: "#go", Text: "the new topic", Nick: "op"},
		},
		{
			// RPL_TOPIC delivered on join: no setter, so Nick stays empty.
			name: "rpl_topic on join",
			raw:  ":serv 332 me #go :the existing topic",
			ok:   true,
			want: core.Event{Type: core.EvTopic, Network: "n", Buffer: "#go", Text: "the existing topic", Nick: ""},
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
			if got.Buffer != tt.want.Buffer {
				t.Errorf("Channel = %q, want %q", got.Buffer, tt.want.Buffer)
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
	// classified as an inbound display copy (EvMessageIn + Self), not a new
	// outbound — so it isn't re-sent.
	out := girc.ParseEvent("@batch=1 :me!u@h PRIVMSG alice :hi back")
	out.Echo = true
	ev, ok = toEvent("n", out, self)
	if !ok {
		t.Fatal("echo event not ok")
	}
	if ev.Type != core.EvMessageIn {
		t.Errorf("Type = %q, want message_in", ev.Type)
	}
	if ev.Message.Buffer != "alice" {
		t.Errorf("echo DM buffer = %q, want alice", ev.Message.Buffer)
	}
	if !ev.Message.Self {
		t.Error("echo message Self = false, want true")
	}
}

func TestToEventAway(t *testing.T) {
	// Now-away (trailing message present).
	ev, ok := toEvent("n", girc.ParseEvent(":alice!u@h AWAY :lunch"), "me")
	if !ok || ev.Type != core.EvAway || ev.Nick != "alice" || !ev.Away {
		t.Fatalf("away event = %+v ok=%v", ev, ok)
	}
	// Back (no trailing message).
	ev, ok = toEvent("n", girc.ParseEvent(":alice!u@h AWAY"), "me")
	if !ok || ev.Type != core.EvAway || ev.Away {
		t.Fatalf("back event = %+v ok=%v", ev, ok)
	}
}

func TestChannelModeEvent(t *testing.T) {
	const prefix = "(qaohv)~&@%+"
	const chanmodes = "eIbq,k,flj,CFLMPQScgimnprstuz" // Libera-ish

	tests := []struct {
		name string
		raw  string
		want []core.MemberMode
		ok   bool
	}{
		{
			name: "op",
			raw:  ":Chan!u@h MODE #c +o alice",
			want: []core.MemberMode{{Nick: "alice", Symbol: "@", Add: true}},
			ok:   true,
		},
		{
			name: "deop",
			raw:  ":Chan!u@h MODE #c -o alice",
			want: []core.MemberMode{{Nick: "alice", Symbol: "@", Add: false}},
			ok:   true,
		},
		{
			name: "mixed op and voice with a toggle",
			raw:  ":Chan!u@h MODE #c +o-v alice bob",
			want: []core.MemberMode{
				{Nick: "alice", Symbol: "@", Add: true},
				{Nick: "bob", Symbol: "+", Add: false},
			},
			ok: true,
		},
		{
			// A setting mode with an arg (+k key) must not steal the nick arg
			// that belongs to +o.
			name: "setting arg interleaved with op",
			raw:  ":Chan!u@h MODE #c +ko secret alice",
			want: []core.MemberMode{{Nick: "alice", Symbol: "@", Add: true}},
			ok:   true,
		},
		{
			name: "only channel settings → no membership change",
			raw:  ":Chan!u@h MODE #c +mt",
			ok:   false,
		},
		{
			name: "user mode (not a channel) ignored",
			raw:  ":me MODE me +i",
			ok:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, ok := channelModeEvent("n", girc.ParseEvent(tt.raw), prefix, chanmodes)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v (ev=%+v)", ok, tt.ok, ev)
			}
			if !tt.ok {
				return
			}
			if ev.Type != core.EvMode || ev.Buffer != "#c" || ev.Nick != "Chan" {
				t.Fatalf("ev = %+v", ev)
			}
			if !slices.Equal(ev.MemberModes, tt.want) {
				t.Fatalf("MemberModes = %+v, want %+v", ev.MemberModes, tt.want)
			}
		})
	}
}

// TestToEventNumerics covers the WHOIS/WHO/error replies the engine
// surfaces as system lines. Each entry asserts both the formatted text
// (what the user will see) and the routing subject (which buffer the
// engine pairs the line to via pendingWhois).
func TestToEventNumerics(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantText    string
		wantSubject string
		wantCount   int
	}{
		{
			name:        "RPL_WHOISUSER",
			raw:         ":serv 311 me alice ~auser host.example * :Alice Liddell",
			wantText:    "alice (~auser@host.example): Alice Liddell",
			wantSubject: "alice",
			wantCount:   311,
		},
		{
			name:        "RPL_WHOISCHANNELS",
			raw:         ":serv 319 me alice :@#go +#cats",
			wantText:    "alice on: @#go +#cats",
			wantSubject: "alice",
			wantCount:   319,
		},
		{
			name:        "RPL_WHOISIDLE",
			raw:         ":serv 317 me alice 42 1700000000 :seconds idle, signon time",
			wantText:    "alice: idle 42s, signon 1700000000",
			wantSubject: "alice",
			wantCount:   317,
		},
		{
			name:        "RPL_ENDOFWHOIS",
			raw:         ":serv 318 me alice :End of /WHOIS list",
			wantText:    "End of WHOIS for alice",
			wantSubject: "alice",
			wantCount:   318,
		},
		{
			name:        "ERR_NOSUCHNICK",
			raw:         ":serv 401 me ghost :No such nick/channel",
			wantText:    "no such nick: ghost",
			wantSubject: "ghost",
			wantCount:   401,
		},
		{
			name:        "ERR_CHANOPRIVSNEEDED",
			raw:         ":serv 482 me #ops :You're not a channel operator",
			wantText:    "#ops: you're not a channel operator",
			wantSubject: "#ops",
			wantCount:   482,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, ok := toEvent("n", girc.ParseEvent(tt.raw), "me")
			if !ok {
				t.Fatal("toEvent returned ok=false")
			}
			if ev.Type != core.EvNumeric {
				t.Fatalf("Type = %v, want EvNumeric", ev.Type)
			}
			if ev.Text != tt.wantText {
				t.Errorf("Text = %q, want %q", ev.Text, tt.wantText)
			}
			if ev.Nick != tt.wantSubject {
				t.Errorf("Nick = %q, want %q", ev.Nick, tt.wantSubject)
			}
			if ev.Count != tt.wantCount {
				t.Errorf("Count = %d, want %d", ev.Count, tt.wantCount)
			}
		})
	}
}

func TestToEventTyping(t *testing.T) {
	// Inbound +typing TAGMSG from someone else → EvTyping.
	e := girc.ParseEvent("@+typing=active :alice!u@h TAGMSG #go")
	ev, ok := toEvent("n", e, "me")
	if !ok || ev.Type != core.EvTyping || ev.Nick != "alice" || ev.Buffer != "#go" || ev.Text != "active" {
		t.Fatalf("typing event = %+v ok=%v", ev, ok)
	}
	// Our own typing echo is ignored.
	if _, ok := toEvent("n", girc.ParseEvent("@+typing=active :me!u@h TAGMSG #go"), "me"); ok {
		t.Error("own typing echo should be ignored")
	}
	// A TAGMSG without +typing is ignored.
	if _, ok := toEvent("n", girc.ParseEvent("@+draft/react=x :a!u@h TAGMSG #go"), "me"); ok {
		t.Error("non-typing TAGMSG should be ignored")
	}
}

func TestToEventReact(t *testing.T) {
	// A +draft/react TAGMSG referencing a msgid → EvReact (own echo included).
	e := girc.ParseEvent("@+draft/react=👍;+draft/reply=abc123 :alice!u@h TAGMSG #go")
	ev, ok := toEvent("n", e, "me")
	if !ok || ev.Type != core.EvReact {
		t.Fatalf("react event = %+v ok=%v", ev, ok)
	}
	if ev.Nick != "alice" || ev.Buffer != "#go" || ev.Target != "abc123" || ev.Text != "👍" {
		t.Errorf("react fields = %+v", ev)
	}
	// A react without a +draft/reply target is dropped.
	if _, ok := toEvent("n", girc.ParseEvent("@+draft/react=x :a!u@h TAGMSG #go"), "me"); ok {
		t.Error("react without reply target should be ignored")
	}
	// A direct react routes to the sender's query buffer.
	dm := girc.ParseEvent("@+draft/react=❤;+draft/reply=m1 :bob!u@h TAGMSG me")
	if ev, ok := toEvent("n", dm, "me"); !ok || ev.Buffer != "bob" {
		t.Errorf("direct react buffer = %q ok=%v", ev.Buffer, ok)
	}
}

func TestToEventRedact(t *testing.T) {
	e := girc.ParseEvent(":alice!u@h REDACT #go badmsgid :spam")
	ev, ok := toEvent("n", e, "me")
	if !ok || ev.Type != core.EvRedact {
		t.Fatalf("redact event = %+v ok=%v", ev, ok)
	}
	if ev.Buffer != "#go" || ev.Target != "badmsgid" || ev.Nick != "alice" || ev.Text != "spam" {
		t.Errorf("redact fields = %+v", ev)
	}
	// Reason is optional.
	if ev, ok := toEvent("n", girc.ParseEvent(":alice!u@h REDACT #go m2"), "me"); !ok || ev.Target != "m2" || ev.Text != "" {
		t.Errorf("redact w/o reason = %+v ok=%v", ev, ok)
	}
}

func TestToEventStandardReplies(t *testing.T) {
	for _, tt := range []struct{ raw, want string }{
		{":serv FAIL JOIN CHANNELS_FULL #x :Channel is full", "[FAIL] JOIN CHANNELS_FULL #x: Channel is full"},
		{":serv WARN * ACCOUNT_REQUIRED :log in first", "[WARN] * ACCOUNT_REQUIRED: log in first"},
		{":serv NOTE NICK :registered", "[NOTE] NICK: registered"},
	} {
		ev, ok := toEvent("n", girc.ParseEvent(tt.raw), "me")
		if !ok || ev.Type != core.EvNumeric {
			t.Fatalf("standard reply %q → ok=%v type=%v", tt.raw, ok, ev.Type)
		}
		if ev.Text != tt.want {
			t.Errorf("text = %q, want %q", ev.Text, tt.want)
		}
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
	if ev.Buffer != "#go" {
		t.Errorf("channel = %q", ev.Buffer)
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
