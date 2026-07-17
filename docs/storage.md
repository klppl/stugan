# Storage

`internal/store` is the only package that imports SQLite
(`modernc.org/sqlite`, a pure-Go driver — no cgo). Each user gets one database
at `<data>/stugan.db`. The `Store` implements both `core.Sink` (it persists
every committed line) and `core.NetworkStore` (network + plugin KV
persistence), and serves history/search queries to the server.

## Schema

```sql
CREATE TABLE messages (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  msgid     TEXT NOT NULL DEFAULT '',   -- IRCv3 msgid
  network   TEXT NOT NULL,
  buffer    TEXT NOT NULL,              -- channel or query name
  ts        INTEGER NOT NULL,           -- unix milliseconds (server-time)
  from_nick TEXT NOT NULL DEFAULT '',
  account   TEXT NOT NULL DEFAULT '',
  kind      TEXT NOT NULL,
  text      TEXT NOT NULL,
  self      INTEGER NOT NULL DEFAULT 0,
  highlight INTEGER NOT NULL DEFAULT 0,
  tags      TEXT NOT NULL DEFAULT ''    -- JSON object
);
CREATE INDEX idx_messages_buffer ON messages(network, buffer, id);

-- Full-text search over message text, kept in sync by triggers.
CREATE VIRTUAL TABLE messages_fts USING fts5(
  text, content='messages', content_rowid='id'
);
-- AFTER INSERT / AFTER DELETE triggers mirror rows into messages_fts.

CREATE TABLE networks (
  id   TEXT PRIMARY KEY,
  data TEXT NOT NULL        -- JSON of core.NetworkParams
);

CREATE TABLE plugin_kv (
  script TEXT NOT NULL,
  key    TEXT NOT NULL,
  value  TEXT NOT NULL,
  PRIMARY KEY (script, key)
);

CREATE TABLE read_markers (
  network TEXT NOT NULL,
  buffer  TEXT NOT NULL,
  ts      INTEGER NOT NULL,        -- unix millis; messages newer than this are unread
  PRIMARY KEY (network, buffer)
);
```

Pragmas: `journal_mode=WAL` (concurrent reads alongside the writer),
`busy_timeout=5000`, `foreign_keys=1`.

## Message history

- **`Print(m core.Message)`** — the `Sink` write path. Inserts a row, stamping
  `ts` from `m.Time` (or now) and serializing `m.Tags` to JSON. The FTS index
  follows via trigger.
- **`Backlog(ctx, network, buffer, before, limit)`** — newest-first page ending
  before `before` (or the most recent page when zero). Fetches `limit+1` rows
  to compute the `more` flag, then reverses to oldest-first for the client.
- **`BacklogAround(ctx, network, buffer, around, limit)`** — a window of
  `limit/2` older + `limit/2` newer messages around an anchor time, returned
  oldest-first, with separate flags for history before and after the window.
  Backs jump-to-message and search-result navigation.
- **`Search(ctx, query, network, buffer, limit)`** — an FTS5 MATCH over
  `messages_fts`, newest matches first, optionally scoped to a network/buffer.

These feed the server's `backlog`, `backlog` (windowed), and `search:result`
frames (see [protocol.md](protocol.md)).

## Read markers (unread that survives a reload)

The browser's unread badge is a live, in-memory counter — it would reset to
zero on every page load. `read_markers` makes it durable: one timestamp per
buffer recording how far the user has read.

- **`MarkRead(ctx, network, buffer, ts)`** — upsert the marker to `ts` (zero =
  now), `MAX`-merged so it only ever moves forward; a stale frame can't un-read
  a buffer. Driven by the c2s `read` frame, sent when a buffer is focused and
  (debounced) as messages arrive while it's focused.
- **`UnreadCounts(ctx)`** — per buffer, how many conversational
  (`privmsg`/`notice`/`action`), non-self messages arrived after its marker,
  and how many of those are highlights. Only buffers that *have* a marker are
  reported, so a buffer's existing history is never retroactively counted as
  unread before it's been opened once. The server calls this at connect time
  and folds the counts into the `init` snapshot's `ChannelDTO.unread/highlight`.

## Network persistence

The store is the **source of truth for networks**. Config `[[networks]]` only
seed it on first run; thereafter the GUI manages networks and they live here.

- **`SaveNetwork(p core.NetworkParams)`** — upsert a network's full config
  (address, nick, SASL/cert, server password, perform lines, channels) as JSON.
- **`DeleteNetwork(id)`** — remove it.

`cmd/stugan` loads all stored networks at startup and dials each one.

## Plugin KV

`plugin_kv` backs each script's `stugan.kv` store, scoped by script basename.
The plugin host caches values in memory and writes through on set/delete, so KV
survives both hot-reload and daemon restart:

```go
PluginKVGetAll(script string) map[string]string   // lazy load on first access
PluginKVSet(script, key, value string) error
PluginKVDelete(script, key string) error
```

A thin adapter in `cmd/stugan` narrows the store to the `plugin.KV` seam so the
plugin package never imports `store`.

## Notes

- Redaction is currently **view-only**: an inbound `REDACT` removes the live
  copy but the row stays in `messages` and returns on reload. Persisting
  redactions (tombstone by msgid) is on the [roadmap](ircv3.md).
- Tests use a temp on-disk database and exercise the backlog/search paths
  table-driven under `-race`.
</content>
