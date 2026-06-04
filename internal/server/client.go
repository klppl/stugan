package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/klippelism/stugan/internal/proto"
)

// sendBuffer is the per-client outbound queue depth. A client that falls
// further behind than this is dropped (its socket closed); the browser
// reconnects and replays an init snapshot.
const sendBuffer = 128

// pingInterval is how often the server sends a protocol-level WebSocket ping,
// and pongTimeout how long it waits for the pong before declaring the socket
// dead. This detects a browser that vanished without a close handshake (mobile
// suspend, a dropped radio, a half-open NAT mapping) so the connection is torn
// down promptly instead of lingering until the OS TCP timeout — and doubles as
// keepalive traffic that stops idle proxies from cutting the socket. The
// browser answers protocol pings automatically; no app code runs for them.
const (
	pingInterval = 30 * time.Second
	pongTimeout  = 10 * time.Second
)

// client is one connected browser socket. All writes funnel through
// writePump, since a websocket.Conn is not safe for concurrent writes.
type client struct {
	ws     *websocket.Conn
	send   chan proto.Envelope
	log    *slog.Logger
	user   string
	tenant *Tenant
}

func newClient(ws *websocket.Conn, log *slog.Logger) *client {
	return &client{
		ws:   ws,
		send: make(chan proto.Envelope, sendBuffer),
		log:  log,
	}
}

// trySend enqueues a frame without blocking. If the queue is full the
// connection is closed: the client is too slow and will re-sync on
// reconnect rather than receive a corrupted, gap-ridden stream.
func (c *client) trySend(env proto.Envelope) {
	select {
	case c.send <- env:
	default:
		c.log.Warn("client send buffer full; dropping connection")
		c.ws.CloseNow()
	}
}

// sendError enqueues a correlated error frame (best-effort).
func (c *client) sendError(id, code, msg string) {
	env, err := proto.Frame(proto.TError, proto.WireError{Code: code, Message: msg})
	if err != nil {
		return
	}
	env.ID = id
	c.trySend(env)
}

// writePump drains the send queue to the socket until ctx is cancelled or a
// write fails.
func (c *client) writePump(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case env := <-c.send:
			if err := wsjson.Write(ctx, c.ws, env); err != nil {
				return
			}
		}
	}
}

// pingLoop sends a protocol-level ping every pingInterval and cancels the
// connection (via cancel, which unblocks the read and write pumps) if a pong
// doesn't arrive within pongTimeout. websocket.Conn.Ping is safe to call
// concurrently with the writePump; the pong is processed by the reader, which
// must be running for Ping to return.
func (c *client) pingLoop(ctx context.Context, cancel context.CancelFunc) {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, pcancel := context.WithTimeout(ctx, pongTimeout)
			err := c.ws.Ping(pctx)
			pcancel()
			if err != nil {
				if ctx.Err() == nil {
					c.log.Debug("ws ping failed; closing", "user", c.user, "err", err)
				}
				cancel()
				return
			}
		}
	}
}

// decode unmarshals an envelope's payload into v.
func decode(env proto.Envelope, v any) error {
	return json.Unmarshal(env.D, v)
}
