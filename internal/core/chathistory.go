package core

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Upstream IRCv3 chathistory, client side. Two things use it:
//
//   - the /chathistory command, a thin front end over every subcommand, and
//   - catch-up: after registering, stugan asks the server for what it missed
//     while it was offline (CHATHISTORY TARGETS for conversations nobody told
//     it about, then CHATHISTORY AFTER per buffer from its newest stored line).
//
// Replayed lines come back inside a BATCH and are handled as ordinary inbound
// messages: stored (the msgid unique index drops any line already held) and
// shown. Catch-up only ever asks for lines newer than everything stored, so
// they append in order; BEFORE/BETWEEN/AROUND, which fill holes, arrive in
// request order after the live tail.

const (
	// catchUpLimit is the page size of a catch-up AFTER request.
	catchUpLimit = 100
	// catchUpMaxPages caps how many pages one buffer's catch-up may chain, so
	// a long absence on a busy channel cannot replay without bound.
	catchUpMaxPages = 10
	// targetsLimit caps the conversations a catch-up TARGETS request returns.
	targetsLimit = 100
	// maxChatHistory is the largest limit /chathistory will ask for.
	maxChatHistory = 1000
	// chatHistoryLayout is the wire form of a timestamp reference.
	chatHistoryLayout = "2006-01-02T15:04:05.000Z"
)

// catchUp is the state of one buffer's in-flight catch-up chain.
type catchUp struct {
	pages int       // AFTER requests sent so far
	since time.Time // the reference of the last request; the chain must advance past it
}

// lastChatTimer is the optional HistoryReader extension catch-up needs: the
// time of the newest stored conversational line in a buffer.
type lastChatTimer interface {
	LastChatTime(ctx context.Context, network, buffer string) (time.Time, error)
}

func hasChatHistory(conn IRCConn) bool {
	caps := conn.Caps()
	return slices.Contains(caps, "draft/chathistory") || slices.Contains(caps, "chathistory")
}

// sameConn reports whether a and b are the same connection. Connections are
// pointers, but a comparison of uncomparable dynamic types would panic, so a
// connection we cannot compare counts as unchanged.
func sameConn(a, b IRCConn) (same bool) {
	defer func() {
		if recover() != nil {
			same = true
		}
	}()
	return a == b
}

func chatHistoryTime(t time.Time) string { return t.UTC().Format(chatHistoryLayout) }

func catchUpKey(network, buffer string) string { return network + "\x00" + FoldIRC(buffer) }

// chatHistoryRef turns a user-typed reference into a wire reference: "*",
// "msgid=…" and "timestamp=…" pass through (the latter validated); a bare
// date or RFC 3339 time becomes a timestamp= reference.
func chatHistoryRef(s string) (string, error) {
	switch {
	case s == "*":
		return s, nil
	case strings.HasPrefix(s, "msgid=") && len(s) > len("msgid="):
		return s, nil
	}
	raw := strings.TrimPrefix(s, "timestamp=")
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return "timestamp=" + chatHistoryTime(t), nil
		}
	}
	return "", fmt.Errorf("bad reference %q: use msgid=ID, timestamp=TIME, a date like 2024-05-01, or *", s)
}

const chatHistoryUsage = "usage: /chathistory [latest] [n] | before|after|around <ref> [n] | between <ref> <ref> [n] | targets [<ref> <ref>] [n]  (ref: msgid=ID, timestamp=TIME, a date, or *)"

// chatHistoryLine builds the CHATHISTORY line for a /chathistory invocation in
// buffer. now anchors the default TARGETS window.
func chatHistoryLine(buffer string, args []string, now time.Time) (string, error) {
	sub := "latest"
	if len(args) > 0 {
		if _, err := strconv.Atoi(args[0]); err != nil { // "/chathistory 20" is shorthand for latest
			sub, args = strings.ToLower(args[0]), args[1:]
		}
	}
	// A trailing bare number is the limit.
	limit := 50
	if n := len(args); n > 0 {
		if v, err := strconv.Atoi(args[n-1]); err == nil {
			if v < 1 {
				return "", fmt.Errorf("limit must be at least 1")
			}
			limit, args = min(v, maxChatHistory), args[:n-1]
		}
	}

	refs := func(want int) ([]string, error) {
		if len(args) != want {
			return nil, fmt.Errorf("%s", chatHistoryUsage)
		}
		out := make([]string, want)
		for i, a := range args {
			r, err := chatHistoryRef(a)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}
	needBuffer := func() error {
		if buffer == "" || buffer == StatusBuffer || IsPluginBuffer(buffer) {
			return fmt.Errorf("/chathistory %s works in a channel or query", sub)
		}
		return nil
	}

	switch sub {
	case "latest":
		if err := needBuffer(); err != nil {
			return "", err
		}
		ref := "*"
		if len(args) > 0 {
			r, err := refs(1)
			if err != nil {
				return "", err
			}
			ref = r[0]
		}
		return fmt.Sprintf("CHATHISTORY LATEST %s %s %d", buffer, ref, limit), nil
	case "before", "after", "around":
		if err := needBuffer(); err != nil {
			return "", err
		}
		r, err := refs(1)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("CHATHISTORY %s %s %s %d", strings.ToUpper(sub), buffer, r[0], limit), nil
	case "between":
		if err := needBuffer(); err != nil {
			return "", err
		}
		r, err := refs(2)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("CHATHISTORY BETWEEN %s %s %s %d", buffer, r[0], r[1], limit), nil
	case "targets":
		from, to := "timestamp="+chatHistoryTime(now.Add(-7*24*time.Hour)), "timestamp="+chatHistoryTime(now.Add(time.Minute))
		if len(args) > 0 {
			r, err := refs(2)
			if err != nil {
				return "", err
			}
			from, to = r[0], r[1]
		}
		return fmt.Sprintf("CHATHISTORY TARGETS %s %s %d", from, to, limit), nil
	}
	return "", fmt.Errorf("%s", chatHistoryUsage)
}

// runChatHistory is the /chathistory command.
func (e *Engine) runChatHistory(ev Event, conn IRCConn) {
	say := func(text string) {
		e.inject(Message{
			Network: ev.Network, Buffer: ev.Buffer, Time: time.Now(),
			Kind: MsgSystem, Text: text,
		})
	}
	// IRCv3 server-side history (bouncers/ergo). On servers without it, our
	// SQLite backlog already covers history.
	if !hasChatHistory(conn) {
		say("this server does not support chathistory")
		return
	}
	line, err := chatHistoryLine(ev.Buffer, ev.Args, time.Now())
	if err != nil {
		say(err.Error())
		return
	}
	conn.SendRaw(line)
}

// startCatchUp asks the server for conversations that saw traffic while we
// were away. The reference is the newest stored line across our buffers: every
// missed message is newer than it, so nothing is skipped (a larger window just
// returns a few targets we already have). With nothing stored there is no
// baseline and no gap to fill. Replies arrive as EvHistoryTarget.
func (e *Engine) startCatchUp(network string) {
	lt, ok := e.history.(lastChatTimer)
	if !ok {
		return
	}
	e.mu.RLock()
	conn := e.conns[network]
	var buffers []string
	if n := e.user.Network(network); n != nil && conn != nil {
		for _, c := range n.Channels {
			if c.Kind == KindChannel || c.Kind == KindQuery {
				buffers = append(buffers, c.Name)
			}
		}
	}
	e.mu.RUnlock()
	if conn == nil || !hasChatHistory(conn) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var since time.Time
		for _, b := range buffers {
			if t, err := lt.LastChatTime(ctx, network, b); err == nil && t.After(since) {
				since = t
			}
		}
		if since.IsZero() {
			return
		}
		e.mu.Lock()
		if !sameConn(e.conns[network], conn) {
			e.mu.Unlock() // disconnected or removed while we read the store
			return
		}
		e.targetsSince[network] = since
		e.mu.Unlock()
		conn.SendRaw(fmt.Sprintf("CHATHISTORY TARGETS timestamp=%s timestamp=%s %d",
			chatHistoryTime(since), chatHistoryTime(time.Now().Add(time.Minute)), targetsLimit))
	}()
}

// catchUpBuffer requests the lines in buffer newer than the newest we hold.
// newest, when set, is the server's own idea of the buffer's latest line: if
// we already have it there is nothing to fetch. fallback is the baseline for a
// buffer with no stored lines (a conversation first seen through TARGETS).
func (e *Engine) catchUpBuffer(network, buffer string, newest, fallback time.Time) {
	lt, ok := e.history.(lastChatTimer)
	if !ok {
		return
	}
	e.mu.RLock()
	conn := e.conns[network]
	e.mu.RUnlock()
	if conn == nil || !hasChatHistory(conn) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		since, err := lt.LastChatTime(ctx, network, buffer)
		if err != nil {
			return
		}
		if since.IsZero() {
			since = fallback
		}
		if since.IsZero() || (!newest.IsZero() && !newest.After(since)) {
			return
		}
		e.mu.Lock()
		if !sameConn(e.conns[network], conn) {
			e.mu.Unlock()
			return
		}
		e.catchUps[catchUpKey(network, buffer)] = &catchUp{pages: 1, since: since}
		e.mu.Unlock()
		conn.SendRaw(fmt.Sprintf("CHATHISTORY AFTER %s timestamp=%s %d", buffer, chatHistoryTime(since), catchUpLimit))
	}()
}

// onHistoryTarget handles one TARGETS row: a conversation with traffic we may
// have missed. Channels are caught up when we join them; here only private
// conversations matter, and a new one gets its buffer opened first.
func (e *Engine) onHistoryTarget(ev Event) {
	if ev.Buffer == "" || !IsQueryBuffer(ev.Buffer) || IsPluginBuffer(ev.Buffer) {
		return
	}
	e.mu.RLock()
	n := e.user.Network(ev.Network)
	self := n != nil && EqualIRC(n.Nick, ev.Buffer)
	fallback := e.targetsSince[ev.Network]
	e.mu.RUnlock()
	if n == nil || self {
		return
	}
	e.commit(ev) // opens the query buffer if it is new
	e.catchUpBuffer(ev.Network, ev.Buffer, ev.Time, fallback)
}

// onHistoryBatch continues a catch-up chain when a full page came back, until
// a short page, the page cap, or no forward progress ends it.
func (e *Engine) onHistoryBatch(ev Event) {
	key := catchUpKey(ev.Network, ev.Buffer)
	e.mu.Lock()
	ch := e.catchUps[key]
	conn := e.conns[ev.Network]
	if ch == nil {
		e.mu.Unlock()
		return
	}
	if conn == nil || ev.Count < catchUpLimit || ch.pages >= catchUpMaxPages || !ev.Time.After(ch.since) {
		delete(e.catchUps, key)
		e.mu.Unlock()
		return
	}
	ch.pages++
	ch.since = ev.Time
	e.mu.Unlock()
	conn.SendRaw(fmt.Sprintf("CHATHISTORY AFTER %s timestamp=%s %d", ev.Buffer, chatHistoryTime(ev.Time), catchUpLimit))
}

// dropCatchUps forgets a network's catch-up state (disconnect or removal).
func (e *Engine) dropCatchUps(network string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	prefix := network + "\x00"
	for k := range e.catchUps {
		if strings.HasPrefix(k, prefix) {
			delete(e.catchUps, k)
		}
	}
	delete(e.targetsSince, network)
}
