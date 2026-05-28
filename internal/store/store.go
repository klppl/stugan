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

// ChannelList is a no-op for the store (transient browser data).
func (s *Store) ChannelList(string, []core.ChannelListItem) {}

// Typing is a no-op for the store (ephemeral).
func (s *Store) Typing(string, string, string, string) {}

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

// Backlog returns up to limit messages for a buffer that are older than the
// before cursor (a zero time means the most recent page), oldest-first.
// more reports whether older history remains. The client passes the time of
// the oldest message it holds as the next before cursor to page backward.
func (s *Store) Backlog(ctx context.Context, network, buffer string, before time.Time, limit int) (msgs []core.Message, more bool, err error) {
	if limit <= 0 {
		limit = 100
	}
	beforeMillis := int64(1<<63 - 1)
	if !before.IsZero() {
		beforeMillis = before.UnixMilli()
	}
	// Fetch limit+1 newest-first to detect whether more remain, then reverse.
	// Order by id (insertion order) for a stable sequence; filter by ts so
	// the wire-visible message time can serve as the paging cursor.
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, msgid, network, buffer, ts, from_nick, account, kind, text, self, highlight, tags
		 FROM messages
		 WHERE network = ? AND buffer = ? AND ts < ?
		 ORDER BY id DESC LIMIT ?`,
		network, buffer, beforeMillis, limit+1,
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
	// Reverse to oldest-first.
	msgs = make([]core.Message, len(desc))
	for i, m := range desc {
		msgs[len(desc)-1-i] = m
	}
	return msgs, more, nil
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
		return s.Backlog(ctx, network, buffer, time.Time{}, limit)
	}
	aroundMS := around.UnixMilli()
	beforeHalf := max(limit/2, 1)
	afterHalf := limit - beforeHalf

	// Older half (and the anchor row), newest-first. Fetch beforeHalf+1 so
	// the (limit/2 + 1)-th row tells us whether older history still exists.
	rowsA, err := s.db.QueryContext(ctx,
		`SELECT id, msgid, network, buffer, ts, from_nick, account, kind, text, self, highlight, tags
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
	for i, j := 0, len(older)-1; i < j; i, j = i+1, j-1 {
		older[i], older[j] = older[j], older[i]
	}

	// Newer half, oldest-first.
	rowsB, err := s.db.QueryContext(ctx,
		`SELECT id, msgid, network, buffer, ts, from_nick, account, kind, text, self, highlight, tags
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

// Search runs a full-text query over message text, newest matches first.
// network and/or buffer narrow the scope when non-empty.
func (s *Store) Search(ctx context.Context, query, network, buffer string, limit int) ([]core.Message, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT m.id, m.msgid, m.network, m.buffer, m.ts, m.from_nick, m.account, m.kind, m.text, m.self, m.highlight, m.tags
	      FROM messages_fts f JOIN messages m ON m.id = f.rowid
	      WHERE messages_fts MATCH ?`
	args := []any{query}
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
