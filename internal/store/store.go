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
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"

	"github.com/klippelism/stugan/internal/core"
)

// Store is a SQLite-backed message store.
//
// Message writes (Print, Redact) arrive on the engine loop, so they are not
// executed there: they are queued to a single writer goroutine that commits
// whatever has accumulated in one transaction (group commit). Under a burst
// that turns hundreds of fsyncs into a few, and the engine never waits on the
// disk unless the queue is full. Readers call flush first, so a query always
// sees every line committed before it was issued.
type Store struct {
	db   *sql.DB
	path string
	log  *slog.Logger

	writes     chan writeOp
	writerDone chan struct{}
	closeMu    sync.RWMutex
	closed     bool
	insertStmt *sql.Stmt
}

// writeOp is one queued write: a message to insert, a redaction, or (barrier
// non-nil) a flush marker closed once everything queued before it committed.
type writeOp struct {
	msg     *core.Message
	redact  *redactOp
	barrier chan struct{}
}

type redactOp struct{ network, bufferFold, msgid string }

// writeQueue bounds queued writes; Print blocks only when the disk falls this
// far behind.
const writeQueue = 4096

// maxBatch caps the writes committed in one transaction.
const maxBatch = 1024

// schemaVersion is stored in PRAGMA user_version once the one-time migrations
// below have run, so a restart doesn't repeat full-table rewrites.
const schemaVersion = 2

const insertMessageSQL = `INSERT OR IGNORE INTO messages(msgid, network, buffer, buffer_fold, ts, from_nick, account, kind, text, self, highlight, tags)
	 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

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
  buffer_fold TEXT NOT NULL DEFAULT '', -- RFC1459-folded buffer identity
  ts        INTEGER NOT NULL,            -- unix milliseconds
  from_nick TEXT NOT NULL DEFAULT '',
  account   TEXT NOT NULL DEFAULT '',
  kind      TEXT NOT NULL,
  text      TEXT NOT NULL,
  self      INTEGER NOT NULL DEFAULT 0,
  highlight INTEGER NOT NULL DEFAULT 0,
  tags      TEXT NOT NULL DEFAULT ''     -- JSON object
);
-- (network, buffer_fold, id) and (network, buffer_fold, ts) indexes are
-- created by the migrations in Open, after buffer_fold exists on old DBs.

CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
  text, content='messages', content_rowid='id', tokenize='trigram'
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
	// synchronous=NORMAL is the recommended setting for WAL: a commit no
	// longer fsyncs, the WAL is synced at checkpoints instead. A power loss can
	// drop the last moments of history but never corrupts the database.
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if path == ":memory:" {
		// Every pooled connection to :memory: is a separate, empty database.
		db.SetMaxOpenConns(1)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrateSearchIndex(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate search index: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	ins, err := db.Prepare(insertMessageSQL)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("prepare insert: %w", err)
	}
	s := &Store{
		db: db, path: path, log: log,
		writes:     make(chan writeOp, writeQueue),
		writerDone: make(chan struct{}),
		insertStmt: ins,
	}
	go s.writer()
	return s, nil
}

// migrate brings a database created by an older build up to schemaVersion.
// Each step is a full-table operation, so it runs once per database (gated by
// PRAGMA user_version), not on every start.
func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version >= schemaVersion {
		return nil
	}
	// Tolerant migrations for databases created by earlier versions.
	for _, alter := range []string{
		`ALTER TABLE messages ADD COLUMN highlight INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE messages ADD COLUMN buffer_fold TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.Exec(alter); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	if _, err := db.Exec(`UPDATE messages SET buffer_fold = ` + sqlFoldBuffer + ` WHERE buffer_fold = ''`); err != nil {
		return fmt.Errorf("migrate buffer casemapping: %w", err)
	}
	if err := migrateReadMarkers(db); err != nil {
		return fmt.Errorf("migrate read-marker casemapping: %w", err)
	}
	// Deduplicate rows persisted before msgid uniqueness was enforced
	// (chathistory playback re-inserted the same messages on every run),
	// then add the unique index that makes replays no-ops in Print. The
	// unfolded-buffer indexes are unused (every query keys on buffer_fold)
	// and only slowed inserts; (network, buffer_fold, ts) serves the
	// read-marker queries (unread counts, missed highlights).
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_messages_msgid`,
		`DELETE FROM messages WHERE msgid <> '' AND id NOT IN (
		   SELECT MIN(id) FROM messages WHERE msgid <> '' GROUP BY network, buffer_fold, msgid)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_msgid
		   ON messages(network, buffer_fold, msgid) WHERE msgid <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_messages_buffer_fold ON messages(network, buffer_fold, id)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_fold_ts ON messages(network, buffer_fold, ts)`,
		`DROP INDEX IF EXISTS idx_messages_buffer`,
		`DROP INDEX IF EXISTS idx_messages_buffer_ts`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate indexes: %w", err)
		}
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

const sqlFoldBuffer = `replace(replace(replace(replace(lower(buffer), '[', '{'), ']', '}'), '\', '|'), '~', '^')`
const sqlFoldFrom = `replace(replace(replace(replace(lower(m.from_nick), '[', '{'), ']', '}'), '\', '|'), '~', '^')`

// migrateReadMarkers canonicalizes existing marker keys to the RFC1459 fold.
// Rebuilding is cheap (one row per visited buffer) and safely merges legacy
// duplicate spellings by keeping their furthest read timestamp.
func migrateReadMarkers(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS read_markers_folded`,
		`CREATE TABLE read_markers_folded (
		  network TEXT NOT NULL, buffer TEXT NOT NULL, ts INTEGER NOT NULL,
		  PRIMARY KEY (network, buffer))`,
		`INSERT INTO read_markers_folded(network, buffer, ts)
		 SELECT network, ` + sqlFoldBuffer + `, MAX(ts)
		 FROM read_markers GROUP BY network, ` + sqlFoldBuffer,
		`DROP TABLE read_markers`,
		`ALTER TABLE read_markers_folded RENAME TO read_markers`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// migrateSearchIndex replaces the original word-tokenized FTS table with a
// trigram index. Trigrams retain indexed full-text search while allowing a
// query such as "klimax" to match inside "Antiklimax". CREATE TABLE IF NOT
// EXISTS cannot change the tokenizer of databases created by older builds, so
// detect those databases from sqlite_schema and rebuild their index once.
func migrateSearchIndex(db *sql.DB) error {
	var ddl string
	if err := db.QueryRow(
		`SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'messages_fts'`,
	).Scan(&ddl); err != nil {
		return err
	}
	if strings.Contains(strings.ToLower(ddl), "trigram") {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`DROP TRIGGER IF EXISTS messages_ai`,
		`DROP TRIGGER IF EXISTS messages_ad`,
		`DROP TABLE messages_fts`,
		`CREATE VIRTUAL TABLE messages_fts USING fts5(
		  text, content='messages', content_rowid='id', tokenize='trigram'
		)`,
		`CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
		  INSERT INTO messages_fts(rowid, text) VALUES (new.id, new.text);
		END`,
		`CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
		  INSERT INTO messages_fts(messages_fts, rowid, text) VALUES('delete', old.id, old.text);
		END`,
		`INSERT INTO messages_fts(messages_fts) VALUES('rebuild')`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Close drains the write queue and closes the database.
func (s *Store) Close() error {
	s.closeMu.Lock()
	if !s.closed {
		s.closed = true
		close(s.writes)
	}
	s.closeMu.Unlock()
	<-s.writerDone
	s.insertStmt.Close()
	return s.db.Close()
}

// enqueue hands a write to the writer goroutine. It reports false once the
// store is closed.
func (s *Store) enqueue(op writeOp) bool {
	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed {
		return false
	}
	s.writes <- op
	return true
}

// flush waits until every write queued before it has been committed.
func (s *Store) flush() {
	done := make(chan struct{})
	if s.enqueue(writeOp{barrier: done}) {
		<-done
	}
}

// writer is the single goroutine that executes queued writes, committing
// each accumulated batch in one transaction.
func (s *Store) writer() {
	defer close(s.writerDone)
	batch := make([]writeOp, 0, maxBatch)
	for op := range s.writes {
		batch = append(batch[:0], op)
	fill:
		for len(batch) < maxBatch {
			select {
			case more, ok := <-s.writes:
				if !ok {
					break fill
				}
				batch = append(batch, more)
			default:
				break fill
			}
		}
		s.commitBatch(batch)
		for _, op := range batch {
			if op.barrier != nil {
				close(op.barrier)
			}
		}
	}
}

// commitBatch writes a batch in one transaction, falling back to one
// statement at a time if the transaction cannot be used, so a single bad row
// or a busy database never loses the rest.
func (s *Store) commitBatch(batch []writeOp) {
	n := 0
	for _, op := range batch {
		if op.barrier == nil {
			n++
		}
	}
	if n == 0 {
		return
	}
	tx, err := s.db.Begin()
	if err == nil {
		ins := tx.Stmt(s.insertStmt)
		for _, op := range batch {
			if err = s.execOp(tx, ins, op); err != nil {
				break
			}
		}
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if err == nil {
			return
		}
	}
	s.log.Warn("batched history write failed; retrying row by row", "rows", n, "err", err)
	for _, op := range batch {
		if err := s.execOp(nil, s.insertStmt, op); err != nil {
			s.logWriteError(op, err)
		}
	}
}

// execOp runs one write, inside tx when non-nil.
func (s *Store) execOp(tx *sql.Tx, ins *sql.Stmt, op writeOp) error {
	switch {
	case op.msg != nil:
		m := op.msg
		tags := ""
		if len(m.Tags) > 0 {
			if b, err := json.Marshal(m.Tags); err == nil {
				tags = string(b)
			}
		}
		_, err := ins.Exec(
			m.ID, m.Network, m.Buffer, core.FoldIRC(m.Buffer), m.Time.UnixMilli(), m.From, m.Account,
			string(m.Kind), m.Text, boolToInt(m.Self), boolToInt(m.Highlight), tags,
		)
		return err
	case op.redact != nil:
		r := op.redact
		const q = `DELETE FROM messages WHERE network = ? AND buffer_fold = ? AND msgid = ?`
		var err error
		if tx != nil {
			_, err = tx.Exec(q, r.network, r.bufferFold, r.msgid)
		} else {
			_, err = s.db.Exec(q, r.network, r.bufferFold, r.msgid)
		}
		return err
	}
	return nil
}

func (s *Store) logWriteError(op writeOp, err error) {
	switch {
	case op.msg != nil:
		s.log.Error("persist message", "network", op.msg.Network, "buffer", op.msg.Buffer, "err", err)
	case op.redact != nil:
		s.log.Error("redact message", "network", op.redact.network, "msgid", op.redact.msgid, "err", err)
	}
}

// Print implements core.Sink: it queues a committed buffer line for
// persistence. OR IGNORE on insert: a message with a msgid we already hold
// (chathistory or bouncer replay of a line stored live) is not duplicated.
func (s *Store) Print(m core.Message) {
	if m.Time.IsZero() {
		m.Time = time.Now()
	}
	s.enqueue(writeOp{msg: &m})
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

// React is an ephemeral overlay the store does not persist.
func (s *Store) React(string, string, string, string, string) {}

// Redact removes the target line from history: the message-redaction spec's
// intent is removal, and a line that only disappears live but resurfaces in
// backlog/search after a reload isn't redacted. The server only relays
// REDACTs it accepted (author or op), so no authorship check here. Queued
// behind any pending insert of the same line, so ordering is preserved.
func (s *Store) Redact(network, buffer, target, _, _ string) {
	if target == "" {
		return
	}
	s.enqueue(writeOp{redact: &redactOp{network: network, bufferFold: core.FoldIRC(buffer), msgid: target}})
}

// pruneBatch is how many rows one prune statement deletes. Small batches keep
// each write transaction short so live inserts are never stuck behind a
// retention sweep of millions of rows (and their FTS index entries).
const pruneBatch = 2000

// Prune deletes messages older than before, in every buffer. The FTS delete
// trigger keeps the search index in sync. Returns the number of rows removed.
func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	s.flush()
	var total int64
	for {
		res, err := s.db.ExecContext(ctx,
			`DELETE FROM messages WHERE id IN (SELECT id FROM messages WHERE ts < ? LIMIT ?)`,
			before.UnixMilli(), pruneBatch)
		if err != nil {
			return total, fmt.Errorf("prune messages: %w", err)
		}
		n, _ := res.RowsAffected()
		total += n
		if n < pruneBatch {
			return total, nil
		}
		select { // let queued live writes in between batches
		case <-ctx.Done():
			return total, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

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
		network, core.FoldIRC(buffer), ts.UnixMilli())
	return err
}

// LastChatTime returns the timestamp of the newest stored conversational line
// (privmsg/notice/action) in a buffer, or the zero time if there is none.
// Local system lines are deliberately ignored: they are stamped at reconnect
// time, after any messages missed while offline, so counting them would hide
// the very gap a chathistory catch-up exists to fill.
func (s *Store) LastChatTime(ctx context.Context, network, buffer string) (time.Time, error) {
	s.flush() // see every line queued before this call
	var ts sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(ts) FROM messages
		 WHERE network = ? AND buffer_fold = ? AND kind IN ('privmsg', 'notice', 'action')`,
		network, core.FoldIRC(buffer)).Scan(&ts)
	if err != nil {
		return time.Time{}, fmt.Errorf("last chat time: %w", err)
	}
	if !ts.Valid {
		return time.Time{}, nil
	}
	return time.UnixMilli(ts.Int64), nil
}

// ReadMarker satisfies core.Sink, advancing the read marker for a buffer.
func (s *Store) ReadMarker(network, buffer string, ts time.Time) {
	_ = s.MarkRead(context.Background(), network, buffer, ts)
}

// ReadMarkers returns a map of "network/buffer_fold" -> timestamp (unix milliseconds).
func (s *Store) ReadMarkers(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT network, buffer, ts FROM read_markers`)
	if err != nil {
		return nil, fmt.Errorf("read markers: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int64)
	for rows.Next() {
		var net, buf string
		var ts int64
		if err := rows.Scan(&net, &buf, &ts); err != nil {
			return nil, err
		}
		out[net+"/"+core.FoldIRC(buf)] = ts
	}
	return out, rows.Err()
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
	s.flush() // see every line queued before this call
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.network, MIN(m.buffer), COUNT(*), COALESCE(SUM(m.highlight), 0)
		 FROM messages m
		 JOIN read_markers r ON r.network = m.network AND r.buffer = m.buffer_fold
		 WHERE m.ts > r.ts AND m.self = 0
		   AND m.kind IN ('privmsg', 'notice', 'action')
		 GROUP BY m.network, m.buffer_fold`)
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
	s.flush() // see every line queued before this call
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+msgColsM+`
		 FROM messages m
		 JOIN read_markers r ON r.network = m.network AND r.buffer = m.buffer_fold
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
	s.flush() // see every line queued before this call
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
		 WHERE network = ? AND buffer_fold = ? AND id < ?
		 ORDER BY id DESC LIMIT ?`,
		network, core.FoldIRC(buffer), beforeSeq, limit+1,
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

// BacklogAround returns a window centered on an exact, scoped message id,
// oldest-first. If the anchor has been pruned or was produced by an older
// client, around is used as a timestamp fallback. Roughly limit/2 messages
// through the anchor plus limit/2 newer messages are returned. more reports
// whether older history exists before the window.
//
// Used to land the user on the conversation surrounding a specific
// message — e.g. clicking a mention — in a single round trip.
func (s *Store) BacklogAround(ctx context.Context, network, buffer, anchor string, around time.Time, limit int) ([]core.Message, bool, bool, error) {
	s.flush() // see every line queued before this call
	if limit <= 0 {
		limit = 100
	}
	var anchorSeq int64
	if anchor != "" {
		err := s.db.QueryRowContext(ctx,
			`SELECT id FROM messages WHERE network = ? AND buffer_fold = ? AND msgid = ?`,
			network, core.FoldIRC(buffer), anchor,
		).Scan(&anchorSeq)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, false, false, fmt.Errorf("resolve backlog anchor: %w", err)
		}
	}
	if anchorSeq == 0 && around.IsZero() {
		// Equivalent to "give me the most recent page" — defer to Backlog
		// so callers don't have to branch.
		msgs, more, err := s.Backlog(ctx, network, buffer, 0, limit)
		return msgs, more, false, err
	}
	beforeHalf := max(limit/2, 1)
	afterHalf := limit - beforeHalf
	olderBoundary := "ts <= ?"
	newerBoundary := "ts > ?"
	boundary := any(around.UnixMilli())
	if anchorSeq != 0 {
		olderBoundary = "id <= ?"
		newerBoundary = "id > ?"
		boundary = anchorSeq
	}

	// Older half (and the anchor row), newest-first. Fetch beforeHalf+1 so
	// the (limit/2 + 1)-th row tells us whether older history still exists.
	rowsA, err := s.db.QueryContext(ctx,
		`SELECT `+msgCols+`
		 FROM messages
		 WHERE network = ? AND buffer_fold = ? AND `+olderBoundary+`
		 ORDER BY id DESC LIMIT ?`,
		network, core.FoldIRC(buffer), boundary, beforeHalf+1,
	)
	if err != nil {
		return nil, false, false, fmt.Errorf("backlog around (older): %w", err)
	}
	defer rowsA.Close()
	var older []core.Message
	for rowsA.Next() {
		m, _, err := scanMessage(rowsA)
		if err != nil {
			return nil, false, false, err
		}
		older = append(older, m)
	}
	if err := rowsA.Err(); err != nil {
		return nil, false, false, err
	}
	more := false
	if len(older) > beforeHalf {
		more = true
		older = older[:beforeHalf]
	}
	// Reverse to oldest-first so it concatenates naturally with the newer
	// half (which we already query in ascending order).
	slices.Reverse(older)

	// Newer half, oldest-first. Fetch one extra so callers can distinguish a
	// genuinely historical window from one that already reaches the live tail.
	rowsB, err := s.db.QueryContext(ctx,
		`SELECT `+msgCols+`
		 FROM messages
		 WHERE network = ? AND buffer_fold = ? AND `+newerBoundary+`
		 ORDER BY id ASC LIMIT ?`,
		network, core.FoldIRC(buffer), boundary, afterHalf+1,
	)
	if err != nil {
		return nil, false, false, fmt.Errorf("backlog around (newer): %w", err)
	}
	defer rowsB.Close()
	var newer []core.Message
	for rowsB.Next() {
		m, _, err := scanMessage(rowsB)
		if err != nil {
			return nil, false, false, err
		}
		newer = append(newer, m)
	}
	if err := rowsB.Err(); err != nil {
		return nil, false, false, err
	}
	moreNewer := len(newer) > afterHalf
	if moreNewer {
		newer = newer[:afterHalf]
	}
	return append(older, newer...), more, moreNewer, nil
}

// searchQuery turns free text into an FTS5 MATCH expression plus any terms too
// short for the trigram index. Each term is quoted so FTS syntax is always
type searchFilter struct {
	cleanQuery string
	from       string
	inBuf      string
	hasLink    bool
	beforeTS   int64
	afterTS    int64
}

// parseSearchFilters extracts structured qualifiers (from:nick, in:#chan, has:link,
// before:YYYY-MM-DD, after:YYYY-MM-DD) out of a raw search string.
func parseSearchFilters(raw string) searchFilter {
	fields := strings.Fields(raw)
	var textTerms []string
	var sf searchFilter

	for _, f := range fields {
		key, val, found := strings.Cut(f, ":")
		if !found || val == "" {
			textTerms = append(textTerms, f)
			continue
		}
		key = strings.ToLower(key)
		switch key {
		case "from", "by", "author":
			sf.from = val
		case "in", "buffer", "chan", "channel":
			sf.inBuf = val
		case "has":
			if strings.EqualFold(val, "link") || strings.EqualFold(val, "url") {
				sf.hasLink = true
			} else {
				textTerms = append(textTerms, f)
			}
		case "before":
			if t, err := time.Parse("2006-01-02", val); err == nil {
				sf.beforeTS = t.Add(24 * time.Hour).UnixMilli()
			} else {
				textTerms = append(textTerms, f)
			}
		case "after", "since":
			if t, err := time.Parse("2006-01-02", val); err == nil {
				sf.afterTS = t.UnixMilli()
			} else {
				textTerms = append(textTerms, f)
			}
		default:
			textTerms = append(textTerms, f)
		}
	}
	sf.cleanQuery = strings.Join(textTerms, " ")
	return sf
}

// searchQuery turns free text into an FTS5 MATCH expression plus any terms too
// short for the trigram index. Each term is quoted so FTS syntax is always
// treated literally. Terms remain implicit-AND: every long term must match the
// trigram index and every one/two-character term must occur in the message.
func searchQuery(raw string) (match string, short []string) {
	fields := strings.Fields(raw)
	quoted := make([]string, 0, len(fields))
	for _, f := range fields {
		if utf8.RuneCountInString(f) < 3 {
			short = append(short, f)
			continue
		}
		quoted = append(quoted, `"`+strings.ReplaceAll(f, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " "), short
}

// Search runs a full-text query over message text, newest matches first.
// network and/or buffer narrow the scope when non-empty. Structured query
// filters (from:nick, in:#chan, has:link, before/after dates) are supported.
func (s *Store) Search(ctx context.Context, query, network, buffer string, limit int) ([]core.Message, error) {
	s.flush() // see every line queued before this call
	if limit <= 0 {
		limit = 50
	}
	sf := parseSearchFilters(query)
	match, short := searchQuery(sf.cleanQuery)
	if match == "" && len(short) == 0 && sf.from == "" && sf.inBuf == "" && !sf.hasLink && sf.beforeTS == 0 && sf.afterTS == 0 {
		return nil, nil // no usable search terms or filters
	}
	q := `SELECT ` + msgColsM + ` FROM messages m`
	var args []any
	if match != "" {
		q += ` JOIN messages_fts f ON m.id = f.rowid WHERE messages_fts MATCH ?`
		args = append(args, match)
	} else {
		q += ` WHERE 1 = 1`
	}
	// FTS5 trigrams cannot match terms shorter than three characters. Preserve
	// useful searches such as "go" with a case-insensitive substring fallback;
	// longer terms still narrow the candidate rows through the index first.
	for _, term := range short {
		q += ` AND instr(lower(m.text), lower(?)) > 0`
		args = append(args, term)
	}
	if network != "" {
		q += " AND m.network = ?"
		args = append(args, network)
	}
	if buffer != "" {
		q += " AND m.buffer_fold = ?"
		args = append(args, core.FoldIRC(buffer))
	} else if sf.inBuf != "" {
		q += " AND m.buffer_fold = ?"
		args = append(args, core.FoldIRC(sf.inBuf))
	}
	if sf.from != "" {
		q += " AND " + sqlFoldFrom + " = ?"
		args = append(args, core.FoldIRC(sf.from))
	}
	if sf.hasLink {
		q += " AND (m.text LIKE '%http://%' OR m.text LIKE '%https://%')"
	}
	if sf.beforeTS > 0 {
		q += " AND m.ts < ?"
		args = append(args, sf.beforeTS)
	}
	if sf.afterTS > 0 {
		q += " AND m.ts >= ?"
		args = append(args, sf.afterTS)
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

// Path returns the filesystem path of the database.
func (s *Store) Path() string { return s.path }

// Backup creates a clean, consistent, standalone snapshot of the database
// at destPath using SQLite's VACUUM INTO.
func (s *Store) Backup(ctx context.Context, destPath string) error {
	s.flush() // see every line queued before this call
	_ = os.Remove(destPath)
	query := fmt.Sprintf("VACUUM INTO '%s'", strings.ReplaceAll(destPath, "'", "''"))
	if _, err := s.db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("backup database: %w", err)
	}
	return nil
}

// importMessagesSQL builds the INSERT … SELECT that copies import_db.messages
// into messages by column name. Columns an older backup lacks get their
// defaults; keepIDs preserves row ids (replace mode, so stored cursors stay
// valid) and otherwise lets new ids be assigned (merge).
func importMessagesSQL(ctx context.Context, tx *sql.Tx, keepIDs bool) (string, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA import_db.table_info(messages)`)
	if err != nil {
		return "", fmt.Errorf("inspect import messages: %w", err)
	}
	src := map[string]bool{}
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, typ        string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return "", err
		}
		src[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	for _, req := range []string{"network", "buffer", "ts", "kind", "text"} {
		if !src[req] {
			return "", fmt.Errorf("import messages: backup lacks column %q", req)
		}
	}
	cols := []struct{ name, def string }{
		{"msgid", "''"}, {"network", ""}, {"buffer", ""}, {"buffer_fold", "''"}, {"ts", ""},
		{"from_nick", "''"}, {"account", "''"}, {"kind", ""}, {"text", ""},
		{"self", "0"}, {"highlight", "0"}, {"tags", "''"},
	}
	if keepIDs && src["id"] {
		cols = append([]struct{ name, def string }{{"id", ""}}, cols...)
	}
	var dst, sel []string
	for _, c := range cols {
		dst = append(dst, c.name)
		if src[c.name] {
			sel = append(sel, c.name)
		} else {
			sel = append(sel, c.def)
		}
	}
	return "INSERT OR IGNORE INTO messages(" + strings.Join(dst, ", ") + ") SELECT " +
		strings.Join(sel, ", ") + " FROM import_db.messages", nil
}

// ImportMode specifies whether imported data replaces or merges into existing tables.
type ImportMode string

const (
	ImportModeReplace ImportMode = "replace"
	ImportModeMerge   ImportMode = "merge"
)

// Import imports data from a source SQLite database at srcPath into the store.
func (s *Store) Import(ctx context.Context, srcPath string, mode ImportMode) error {
	s.flush() // see every line queued before this call
	srcDSN := "file:" + srcPath + "?mode=ro"
	srcDB, err := sql.Open("sqlite", srcDSN)
	if err != nil {
		return fmt.Errorf("open import db: %w", err)
	}
	defer srcDB.Close()

	if err := srcDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping import db: %w", err)
	}

	var quickCheck string
	if err := srcDB.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&quickCheck); err != nil || quickCheck != "ok" {
		return fmt.Errorf("import db corrupted or invalid: %v (quick_check=%s)", err, quickCheck)
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire db conn: %w", err)
	}
	defer conn.Close()

	attachQuery := fmt.Sprintf("ATTACH DATABASE '%s' AS import_db", strings.ReplaceAll(srcPath, "'", "''"))
	if _, err := conn.ExecContext(ctx, attachQuery); err != nil {
		return fmt.Errorf("attach import db: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(ctx, "DETACH DATABASE import_db")
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin import tx: %w", err)
	}
	defer tx.Rollback()

	if mode == ImportModeReplace {
		for _, tbl := range []string{"messages", "networks", "read_markers", "plugin_kv", "prefs"} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+tbl); err != nil {
				return fmt.Errorf("clear table %s: %w", tbl, err)
			}
		}
	}

	// Every copy names its columns: a backup's column order depends on the
	// build that created it (older databases got highlight/buffer_fold
	// appended by ALTER TABLE), so a positional SELECT * would silently put
	// values in the wrong columns. In replace mode the tables are empty, so the
	// merge statements below are exact copies.
	has := func(tbl string) bool {
		var n int
		_ = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM import_db.sqlite_master WHERE type='table' AND name=?", tbl).Scan(&n)
		return n > 0
	}
	for _, c := range []struct{ tbl, cols string }{
		{"networks", "id, data"},
		{"plugin_kv", "script, key, value"},
		{"prefs", "key, value"},
	} {
		if !has(c.tbl) {
			continue
		}
		q := fmt.Sprintf("INSERT OR REPLACE INTO %s(%s) SELECT %s FROM import_db.%s", c.tbl, c.cols, c.cols, c.tbl)
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("import %s: %w", c.tbl, err)
		}
	}
	if has("read_markers") {
		// Fold buffer names (legacy backups hold raw spellings) and keep the
		// furthest marker per buffer.
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO read_markers(network, buffer, ts)
			SELECT i.network, i.fb, MAX(i.ts, COALESCE(r.ts, 0))
			FROM (SELECT network, `+sqlFoldBuffer+` AS fb, MAX(ts) AS ts
			      FROM import_db.read_markers GROUP BY network, fb) i
			LEFT JOIN read_markers r ON r.network = i.network AND r.buffer = i.fb`); err != nil {
			return fmt.Errorf("import read_markers: %w", err)
		}
	}
	if has("messages") {
		q, err := importMessagesSQL(ctx, tx, mode == ImportModeReplace)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("import messages: %w", err)
		}
		// Rows from a backup that predates buffer_fold arrive with it empty.
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET buffer_fold = `+sqlFoldBuffer+` WHERE buffer_fold = ''`); err != nil {
			return fmt.Errorf("fold imported buffers: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit import tx: %w", err)
	}

	_, _ = s.db.ExecContext(ctx, "INSERT INTO messages_fts(messages_fts) VALUES('rebuild')")

	if _, err := conn.ExecContext(ctx, "DETACH DATABASE import_db"); err != nil {
		return fmt.Errorf("detach import db: %w", err)
	}

	return nil
}
