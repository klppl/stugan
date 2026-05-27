package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/klippelism/stugan/internal/core"
	"github.com/klippelism/stugan/internal/proto"
)

// fakeConn is a core.IRCConn that blocks in Connect and records sends.
type fakeConn struct{ sent chan [2]string }

func (f *fakeConn) Connect(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
func (f *fakeConn) SendRaw(string) error              { return nil }
func (f *fakeConn) Message(target, text string) error {
	f.sent <- [2]string{target, text}
	return nil
}
func (f *fakeConn) Caps() []string      { return nil }
func (f *fakeConn) CurrentNick() string { return "me" }
func (f *fakeConn) Close() error        { return nil }

type noopSink struct{}

func (noopSink) Print(core.Message)                         {}
func (noopSink) NetworkChanged(*core.Network)               {}
func (noopSink) NetworkRemoved(string)                      {}
func (noopSink) ChannelList(string, []core.ChannelListItem) {}
func (noopSink) Typing(string, string, string, string)      {}

// fakeHistory returns a canned backlog page.
type fakeHistory struct {
	msgs []core.Message
	more bool
}

func (f *fakeHistory) Backlog(_ context.Context, _, _ string, _ time.Time, _ int) ([]core.Message, bool, error) {
	return f.msgs, f.more, nil
}
func (f *fakeHistory) Search(_ context.Context, _, _, _ string, _ int) ([]core.Message, error) {
	return f.msgs, nil
}

// readFrame reads one envelope with a timeout.
func readFrame(t *testing.T, ctx context.Context, ws *websocket.Conn) proto.Envelope {
	t.Helper()
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var env proto.Envelope
	if err := wsjson.Read(rctx, ws, &env); err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return env
}

func TestWebSocketLoop(t *testing.T) {
	fc := &fakeConn{sent: make(chan [2]string, 1)}

	eng := core.New(core.Options{Sink: noopSink{}})
	srv := New(SingleUser(&Tenant{Engine: eng}), Options{})
	eng.AddSink(srv.Sink(defaultUser))
	eng.AddNetwork(core.NetworkParams{ID: "libera", Name: "libera", Nick: "me"}, fc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"

	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.CloseNow()

	// 1. hello
	if env := readFrame(t, ctx, ws); env.T != proto.THello {
		t.Fatalf("first frame = %q, want hello", env.T)
	}

	// 2. init snapshot with our network
	env := readFrame(t, ctx, ws)
	if env.T != proto.TInit {
		t.Fatalf("second frame = %q, want init", env.T)
	}
	var init proto.InitState
	if err := decode(env, &init); err != nil {
		t.Fatalf("decode init: %v", err)
	}
	if len(init.Networks) != 1 || init.Networks[0].ID != "libera" {
		t.Fatalf("init networks = %+v", init.Networks)
	}

	// 3. send input
	send, _ := proto.Frame(proto.TMsgSend, proto.MsgSend{
		Network: "libera", Buffer: "#go", Text: "hello world",
	})
	if err := wsjson.Write(ctx, ws, send); err != nil {
		t.Fatalf("write msg:send: %v", err)
	}

	// 4. receive the locally-echoed message
	env = readFrame(t, ctx, ws)
	if env.T != proto.TMsg {
		t.Fatalf("frame = %q, want msg", env.T)
	}
	var m proto.MessageDTO
	if err := decode(env, &m); err != nil {
		t.Fatalf("decode msg: %v", err)
	}
	if m.Buffer != "#go" || m.Text != "hello world" || !m.Self || m.From != "me" {
		t.Fatalf("echoed msg = %+v", m)
	}

	// 5. the send reached IRC
	select {
	case got := <-fc.sent:
		if got[0] != "#go" || got[1] != "hello world" {
			t.Fatalf("conn.Message got %v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("conn.Message was not called")
	}
}

func TestBacklogReplay(t *testing.T) {
	hist := &fakeHistory{
		msgs: []core.Message{
			{Network: "n", Buffer: "#c", From: "a", Kind: core.MsgPrivmsg, Text: "old1", Time: time.Now()},
			{Network: "n", Buffer: "#c", From: "b", Kind: core.MsgPrivmsg, Text: "old2", Time: time.Now()},
		},
		more: true,
	}
	eng := core.New(core.Options{Sink: noopSink{}})
	srv := New(SingleUser(&Tenant{Engine: eng, History: hist}), Options{})
	eng.AddSink(srv.Sink(defaultUser))
	eng.AddNetwork(core.NetworkParams{ID: "n", Name: "n", Nick: "me"}, &fakeConn{sent: make(chan [2]string, 1)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.CloseNow()
	readFrame(t, ctx, ws) // hello
	readFrame(t, ctx, ws) // init

	req, _ := proto.Frame(proto.TBacklogFetch, proto.BacklogFetch{Network: "n", Buffer: "#c", Limit: 50})
	req.ID = "req-1"
	if err := wsjson.Write(ctx, ws, req); err != nil {
		t.Fatal(err)
	}
	env := readFrame(t, ctx, ws)
	if env.T != proto.TBacklog {
		t.Fatalf("frame = %q, want backlog", env.T)
	}
	if env.ID != "req-1" {
		t.Errorf("reply id = %q, want req-1 (uncorrelated)", env.ID)
	}
	var resp proto.BacklogResp
	if err := decode(env, &resp); err != nil {
		t.Fatalf("decode backlog: %v", err)
	}
	if resp.Buffer != "#c" || len(resp.Messages) != 2 || !resp.More {
		t.Fatalf("backlog resp = %+v", resp)
	}
	if resp.Messages[0].Text != "old1" || resp.Messages[1].Text != "old2" {
		t.Errorf("backlog order = %q,%q", resp.Messages[0].Text, resp.Messages[1].Text)
	}
}

func TestBacklogWithoutHistory(t *testing.T) {
	eng := core.New(core.Options{Sink: noopSink{}})
	srv := New(SingleUser(&Tenant{Engine: eng}), Options{}) // no History
	eng.AddSink(srv.Sink(defaultUser))
	eng.AddNetwork(core.NetworkParams{ID: "n", Name: "n", Nick: "me"}, &fakeConn{sent: make(chan [2]string, 1)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.CloseNow()
	readFrame(t, ctx, ws) // hello
	readFrame(t, ctx, ws) // init
	req, _ := proto.Frame(proto.TBacklogFetch, proto.BacklogFetch{Network: "n", Buffer: "#c"})
	_ = wsjson.Write(ctx, ws, req)
	if env := readFrame(t, ctx, ws); env.T != proto.TError {
		t.Fatalf("frame = %q, want error", env.T)
	}
}

func TestNetInfo(t *testing.T) {
	eng := core.New(core.Options{Sink: noopSink{}})
	srv := New(SingleUser(&Tenant{Engine: eng}), Options{})
	eng.AddSink(srv.Sink(defaultUser))
	eng.AddNetwork(core.NetworkParams{
		ID: "libera", Name: "libera", Addr: "irc.libera.chat:6697", TLS: true,
		Nick: "me", SASLUser: "acct", Channels: []string{"#go"},
	}, &fakeConn{sent: make(chan [2]string, 1)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	readFrame(t, ctx, ws) // hello
	readFrame(t, ctx, ws) // init

	req, _ := proto.Frame(proto.TNetInfo, proto.NetInfoReq{Network: "libera"})
	req.ID = "i1"
	_ = wsjson.Write(ctx, ws, req)
	env := readFrame(t, ctx, ws)
	if env.T != proto.TNetInfo || env.ID != "i1" {
		t.Fatalf("frame = %q id=%q, want net:info i1", env.T, env.ID)
	}
	var cfg proto.NetConfig
	if err := decode(env, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "irc.libera.chat:6697" || !cfg.TLS || cfg.SASLUser != "acct" || len(cfg.Channels) != 1 {
		t.Fatalf("net:info config = %+v", cfg)
	}
}

func TestRejectsBadMsgSend(t *testing.T) {
	eng := core.New(core.Options{Sink: noopSink{}})
	srv := New(SingleUser(&Tenant{Engine: eng}), Options{})
	eng.AddSink(srv.Sink(defaultUser))
	eng.AddNetwork(core.NetworkParams{ID: "n", Name: "n", Nick: "me"}, &fakeConn{sent: make(chan [2]string, 1)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"

	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.CloseNow()
	readFrame(t, ctx, ws) // hello
	readFrame(t, ctx, ws) // init

	// Missing fields → expect an error frame.
	send, _ := proto.Frame(proto.TMsgSend, proto.MsgSend{Network: "n"})
	if err := wsjson.Write(ctx, ws, send); err != nil {
		t.Fatal(err)
	}
	env := readFrame(t, ctx, ws)
	if env.T != proto.TError {
		t.Fatalf("frame = %q, want error", env.T)
	}
}
