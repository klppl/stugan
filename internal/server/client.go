package server

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/klippelism/stugan/internal/proto"
)

// sendBuffer is the per-client outbound queue depth. A client that falls
// further behind than this is dropped (its socket closed); the browser
// reconnects and replays an init snapshot.
const sendBuffer = 128

// client is one connected browser socket. All writes funnel through
// writePump, since a websocket.Conn is not safe for concurrent writes.
type client struct {
	ws   *websocket.Conn
	send chan proto.Envelope
	log  *slog.Logger
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

// decode unmarshals an envelope's payload into v.
func decode(env proto.Envelope, v any) error {
	return json.Unmarshal(env.D, v)
}
