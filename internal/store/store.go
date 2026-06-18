// Package store persists message history in a single SQLite database
// (pure-Go modernc.org/sqlite) with FTS5 full-text search. It implements
// core.Sink to capture committed buffer lines, and exposes Backlog (for
// replay, Phase 4) and Search (Phase 6). It depends on core for the message
// type; core never imports store.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/klippelism/stugan/internal/core"
)

// Store is a SQLite-backed message store.
type Store struct {
	db  *sql.DB
	log *slog.Logger
}

var (
	_ core.Sink         = (*Store)(nil)
	_ core.NetworkStore = (*Store)(nil)
)

const schema = `
CREATE TABLE IF NOT EXISTS messages (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  msgid     TEXT NOT NULL DEFAULT '',
  network   TEXT NOT NULL,
  buffer    TEXT NOT NULL,
  ts        INTEGER NOT NULL,            -- unix milliseconds
  from_nick TEXT NOT NULL DEFAULT '',
  account   TEXT NOT NULL DEFAULT '',
  kind      TEXT NOT NULL,
  text      TEXT NOT NULL,
  self      INTEGER NOT NULL DEFAULT 0,
  highlight INTEGER NOT NULL DEFAULT 0,
  tags      TEXT NOT NULL DEFAULT ''     -- JSON object
);
CREATE INDEX IF NOT EXISTS idx_messages_buffer ON messages(network, buffer, id);

CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
  text, content='messages', content_rowid='id'
);
CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, text) VALUES (new.id, new.text);
END;
CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, text) VALUES('delete', old.id, old.text);
END;

CREATE TABLE IF NOT EXISTS networks (
  id   TEXT PRIMARY KEY,
  data TEXT NOT NULL      -- JSON of core.NetworkParams
);

CREATE TABLE IF NOT EXISTS plugin_kv (
  script TEXT NOT NULL,
  key    TEXT NOT NULL,
  value  TEXT NOT NULL,
  PRIMARY KEY (script, key)
);

CREATE TABLE IF NOT EXISTS read_markers (
  network TEXT NOT NULL,
  buffer  TEXT NOT NULL,
  ts      INTEGER NOT NULL,        -- unix millis; messages with ts > this are unread
  PRIMARY KEY (network, buffer)
);

CREATE TABLE IF NOT EXISTS prefs (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL             -- opaque JSON owned by the server (highlight rules, mutes)
);
`

// Open opens (creating if needed) the database at path and applies the
// schema. Use ":memory:" for an ephemeral store.
func Open(path string, log *slog.Logger) (*Store, error) {
	if log == nil {
		log = slog.Default()
	}
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	// Tolerant migrations for databases created by earlier versions.
	for _, alter := range []string{
		`ALTER TABLE messages ADD COLUMN highlight INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := db.Exec(alter); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	return &Store{db: db, log: log}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Print implements core.Sink: it persists a committed buffer line.
func (s *Store) Print(m core.Message) {
	tags := ""
	if len(m.Tags) > 0 {
		if b, err := json.Marshal(m.Tags); err == nil {
			tags = string(b)
		}
	}
	ts := m.Time
	if ts.IsZero() {
		ts = time.Now()
	}
	_, err := s.db.Exec(
		`INSERT INTO messages(msgid, network, buffer, ts, from_nick, account, kind, text, self, highlight, tags)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Network, m.Buffer, ts.UnixMilli(), m.From, m.Account,
		string(m.Kind), m.Text, boolToInt(m.Self), boolToInt(m.Highlight), tags,
	)
	if err != nil {
		s.log.Error("persist message", "network", m.Network, "buffer", m.Buffer, "err", err)
	}
}

// NetworkChanged is a no-op for the store: only committed lines are saved.
func (s *Store) NetworkChanged(*core.Network) {}

// NetworkRemoved is a no-op for the store; removal is persisted via
// DeleteNetwork.
func (s *Store) NetworkRemoved(string) {}

// NetworksReordered is a no-op for the store; the new order is persisted via
// SaveNetwork (each network's Pos) by the engine.
func (s *Store) NetworksReordered([]string) {}

// ChannelList is a no-op for the store (transient browser data).
func (s *Store) ChannelList(string, []core.ChannelListItem) {}

// Typing is a no-op for the store (ephemeral).
func (s *Store) Typing(string, string, string, string) {}

// React and Redact are ephemeral overlays the store does not persist.
func (s *Store) React(string, string, string, string, string)  {}
func (s *Store) Redact(string, string, string, string, string) {}

// SaveNetwork upserts a persisted network (core.NetworkStore).
func (s *Store) SaveNetwork(p core.NetworkParams) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO networks(id, data) VALUES(?, ?)
		ON CONFLICT(id) DO UPDATE SET data = excluded.data`, p.ID, string(data))
	return err
}

// DeleteNetwork removes a persisted network (core.NetworkStore).
func (s *Store) DeleteNetwork(id string) error {
	_, err := s.db.Exec(`DELETE FROM networks WHERE id = ?`, id)
	return err
}

// PluginKVGetAll loads every persisted key for a script. Plugins call this
// indirectly via the host's lazy cache, which fills on first script access
// and writes-through on every set/delete. Returns an empty map if the
// script has no persisted state.
func (s *Store) PluginKVGetAll(script string) map[string]string {
	rows, err := s.db.Query(`SELECT key, value FROM plugin_kv WHERE script = ?`, script)
	if err != nil {
		s.log.Error("plugin_kv read", "script", script, "err", err)
		return map[string]string{}
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			s.log.Error("plugin_kv scan", "script", script, "err", err)
			return out
		}
		out[k] = v
	}
	return out
}

// PluginKVSet upserts a plugin KV entry. Errors are returned so the host
// can warn-log; the in-memory cache should stay consistent regardless.
func (s *Store) PluginKVSet(script, key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO plugin_kv(script, key, value) VALUES(?, ?, ?)
		 ON CONFLICT(script, key) DO UPDATE SET value = excluded.value`,
		script, key, value)
	return err
}

// PluginKVDelete removes one entry. Missing entries are not an error.
func (s *Store) PluginKVDelete(script, key string) error {
	_, err := s.db.Exec(`DELETE FROM plugin_kv WHERE script = ? AND key = ?`, script, key)
	return err
}

// Pref returns the value stored under key, or "" if there is none. The value
// is opaque JSON the server owns (highlight rules, muted buffers); the store
// treats it as a blob and does not interpret it.
func (s *Store) Pref(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM prefs WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetPref upserts a server preference blob under key.
func (s *Store) SetPref(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO prefs(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// Networks returns all persisted networks.
func (s *Store) Networks() ([]core.NetworkParams, error) {
	rows, err := s.db.Query(`SELECT data FROM networks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.NetworkParams
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var p core.NetworkParams
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MarkRead advances the read marker for a buffer to ts (the moment the user
// last viewed it). A zero ts means "now". The marker only ever moves forward —
// MAX(existing, new) — so a stale or out-of-order frame can never un-read a
// buffer. UnreadCounts tallies messages with a timestamp newer than the marker.
func (s *Store) MarkRead(ctx context.Context, network, buffer string, ts time.Time) error {
	if ts.IsZero() {
		ts = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO read_markers(network, buffer, ts) VALUES(?, ?, ?)
		 ON CONFLICT(network, buffer) DO UPDATE SET ts = MAX(ts, excluded.ts)`,
		network, buffer, ts.UnixMilli())
	return err
}

// UnreadCounts returns, per buffer, how many conversational messages (and how
// many of those are highlights) arrived since the user's read marker. Only
// buffers that have a marker are reported: until a buffer has been read at
// least once there is no baseline, so its existing history is never
// retroactively counted as unread. Self-sent lines and non-conversational
// events (joins, mode changes, etc.) are excluded, matching the browser's
// live counter. Used at connect time to seed unread badges that survive a
// page reload.
func (s *Store) UnreadCounts(ctx context.Context) ([]core.UnreadCount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.network, m.buffer, COUNT(*), COALESCE(SUM(m.highlight), 0)
		 FROM messages m
		 JOIN read_markers r ON r.network = m.network AND r.buffer = m.buffer
		 WHERE m.ts > r.ts AND m.self = 0
		   AND m.kind IN ('privmsg', 'notice', 'action')
		 GROUP BY m.network, m.buffer`)
	if err != nil {
		return nil, fmt.Errorf("unread counts: %w", err)
	}
	defer rows.Close()
	var out []core.UnreadCount
	for rows.Next() {
		var u core.UnreadCount
		if err := rows.Scan(&u.Network, &u.Buffer, &u.Unread, &u.Highlight); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// MissedHighlights returns the highlight lines that arrived since the user's
// read markers, across every buffer, in arrival order (oldest-first by store
// sequence/rowid, like Backlog/Search), capped at limit (newest kept when the
// cap bites). It is the body of the "what you missed" digest: the same marker
// semantics as UnreadCounts (only buffers with a marker, self and
// non-conversational lines excluded), narrowed to highlighted messages and
// returning the full rows rather than a tally. The LIMIT is applied to the
// newest matches (ORDER BY id DESC) and the result reversed so the digest reads
// in arrival order.
func (s *Store) MissedHighlights(ctx context.Context, limit int) ([]core.Message, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+msgColsM+`
		 FROM messages m
		 JOIN read_markers r ON r.network = m.network AND r.buffer = m.buffer
		 WHERE m.ts > r.ts AND m.self = 0 AND m.highlight = 1
		   AND m.kind IN ('privmsg', 'notice', 'action')
		 ORDER BY m.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("missed highlights: %w", err)
	}
	defer rows.Close()
	var out []core.Message
	for rows.Next() {
		m, _, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reverse newest-first → oldest-first so the digest reads top-down in time.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// Backlog returns up to limit messages for a buffer that are older than the
// beforeSeq cursor (a zero or negative cursor means the most recent page),
// oldest-first. more reports whether older history remains. The client passes
// the Seq of the oldest message it holds as the next beforeSeq to page
// backward.
//
// The cursor is the rowid (Seq), not the timestamp, because rows are ordered
// by id and many messages can share a millisecond timestamp (pastes, bot
// bursts). A ts-based cursor would skip every other message sharing the
// boundary millisecond and stall paging; keyset paging on the indexed id is
// exact.
func (s *Store) Backlog(ctx context.Context, network, buffer string, beforeSeq int64, limit int) (msgs []core.Message, more bool, err error) {
	if limit <= 0 {
		limit = 100
	}
	if beforeSeq <= 0 {
		beforeSeq = int64(1<<63 - 1)
	}
	// Fetch limit+1 newest-first to detect whether more remain, then reverse.
	// Order and page by id (insertion order) for an exact, stable sequence.
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+msgCols+`
		 FROM messages
		 WHERE network = ? AND buffer = ? AND id < ?
		 ORDER BY id DESC LIMIT ?`,
		network, buffer, beforeSeq, limit+1,
	)
	if err != nil {
		return nil, false, fmt.Errorf("backlog query: %w", err)
	}
	defer rows.Close()

	var desc []core.Message
	for rows.Next() {
		m, _, err := scanMessage(rows)
		if err != nil {
			return nil, false, err
		}
		desc = append(desc, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(desc) > limit {
		more = true
		desc = desc[:limit]
	}
	slices.Reverse(desc) // oldest-first
	return desc, more, nil
}

// BacklogAround returns a window of messages centered on the around time,
// oldest-first. Roughly limit/2 messages with ts ≤ around (including the
// anchor) plus limit/2 strictly newer are returned. more reports whether
// older history exists before the window, so the client can still page
// upward from the head of the result.
//
// Used to land the user on the conversation surrounding a specific
// message — e.g. clicking a mention — in a single round trip.
func (s *Store) BacklogAround(ctx context.Context, network, buffer string, around time.Time, limit int) ([]core.Message, bool, error) {
	if limit <= 0 {
		limit = 100
	}
	if around.IsZero() {
		// Equivalent to "give me the most recent page" — defer to Backlog
		// so callers don't have to branch.
		return s.Backlog(ctx, network, buffer, 0, limit)
	}
	aroundMS := around.UnixMilli()
	beforeHalf := max(limit/2, 1)
	afterHalf := limit - beforeHalf

	// Older half (and the anchor row), newest-first. Fetch beforeHalf+1 so
	// the (limit/2 + 1)-th row tells us whether older history still exists.
	rowsA, err := s.db.QueryContext(ctx,
		`SELECT `+msgCols+`
		 FROM messages
		 WHERE network = ? AND buffer = ? AND ts <= ?
		 ORDER BY id DESC LIMIT ?`,
		network, buffer, aroundMS, beforeHalf+1,
	)
	if err != nil {
		return nil, false, fmt.Errorf("backlog around (older): %w", err)
	}
	defer rowsA.Close()
	var older []core.Message
	for rowsA.Next() {
		m, _, err := scanMessage(rowsA)
		if err != nil {
			return nil, false, err
		}
		older = append(older, m)
	}
	if err := rowsA.Err(); err != nil {
		return nil, false, err
	}
	more := false
	if len(older) > beforeHalf {
		more = true
		older = older[:beforeHalf]
	}
	// Reverse to oldest-first so it concatenates naturally with the newer
	// half (which we already query in ascending order).
	slices.Reverse(older)

	// Newer half, oldest-first.
	rowsB, err := s.db.QueryContext(ctx,
		`SELECT `+msgCols+`
		 FROM messages
		 WHERE network = ? AND buffer = ? AND ts > ?
		 ORDER BY id ASC LIMIT ?`,
		network, buffer, aroundMS, afterHalf,
	)
	if err != nil {
		return nil, false, fmt.Errorf("backlog around (newer): %w", err)
	}
	defer rowsB.Close()
	for rowsB.Next() {
		m, _, err := scanMessage(rowsB)
		if err != nil {
			return nil, false, err
		}
		older = append(older, m)
	}
	if err := rowsB.Err(); err != nil {
		return nil, false, err
	}
	return older, more, nil
}

// ftsQuery turns a user's free-text search into a safe FTS5 MATCH expression.
// Each whitespace-separated term is wrapped as a quoted FTS5 string literal
// (embedded quotes doubled), so input like "c++", a stray quote, a trailing
// "AND", or a "key:value" is matched literally instead of being parsed as FTS5
// syntax (which would surface to the user as an opaque error). Terms are joined
// by spaces, preserving FTS5's implicit-AND across words. Returns "" when the
// query has no usable terms.
func ftsQuery(raw string) string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}
	for i, f := range fields {
		fields[i] = `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
	}
	return strings.Join(fields, " ")
}

// Search runs a full-text query over message text, newest matches first.
// network and/or buffer narrow the scope when non-empty.
func (s *Store) Search(ctx context.Context, query, network, buffer string, limit int) ([]core.Message, error) {
	if limit <= 0 {
		limit = 50
	}
	match := ftsQuery(query)
	if match == "" {
		return nil, nil // no usable search terms
	}
	q := `SELECT ` + msgColsM + `
	      FROM messages_fts f JOIN messages m ON m.id = f.rowid
	      WHERE messages_fts MATCH ?`
	args := []any{match}
	if network != "" {
		q += " AND m.network = ?"
		args = append(args, network)
	}
	if buffer != "" {
		q += " AND m.buffer = ?"
		args = append(args, buffer)
	}
	q += " ORDER BY m.id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("search query: %w", err)
	}
	defer rows.Close()

	var out []core.Message
	for rows.Next() {
		m, _, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// msgCols is the messages-table column list every SELECT that feeds
// scanMessage must request, in scanMessage's exact scan order. msgColsM is the
// same list aliased to m, for the search query that joins messages AS m.
const (
	msgCols  = "id, msgid, network, buffer, ts, from_nick, account, kind, text, self, highlight, tags"
	msgColsM = "m.id, m.msgid, m.network, m.buffer, m.ts, m.from_nick, m.account, m.kind, m.text, m.self, m.highlight, m.tags"
)

// scanMessage scans one row into a core.Message and its row id.
func scanMessage(rows *sql.Rows) (core.Message, int64, error) {
	var (
		id        int64
		ts        int64
		self      int
		highlight int
		tags      string
		m         core.Message
	)
	if err := rows.Scan(&id, &m.ID, &m.Network, &m.Buffer, &ts, &m.From,
		&m.Account, &m.Kind, &m.Text, &self, &highlight, &tags); err != nil {
		return core.Message{}, 0, err
	}
	m.Time = time.UnixMilli(ts).UTC()
	m.Seq = id
	m.Self = self != 0
	m.Highlight = highlight != 0
	if tags != "" {
		_ = json.Unmarshal([]byte(tags), &m.Tags)
	}
	return m, id, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
