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
  buffer    TEXT NOT NULL,              -- channel or query name, as displayed
  buffer_fold TEXT NOT NULL DEFAULT '', -- RFC1459-folded buffer identity (all lookups key on this)
  ts        INTEGER NOT NULL,           -- unix milliseconds (server-time)
  from_nick TEXT NOT NULL DEFAULT '',
  account   TEXT NOT NULL DEFAULT '',
  kind      TEXT NOT NULL,
  text      TEXT NOT NULL,
  self      INTEGER NOT NULL DEFAULT 0,
  highlight INTEGER NOT NULL DEFAULT 0,
  tags      TEXT NOT NULL DEFAULT ''    -- JSON object
);
-- Indexes (created by the versioned migrations, see below):
CREATE INDEX idx_messages_buffer_fold ON messages(network, buffer_fold, id);
CREATE INDEX idx_messages_fold_ts     ON messages(network, buffer_fold, ts);
CREATE UNIQUE INDEX idx_messages_msgid ON messages(network, buffer_fold, msgid)
  WHERE msgid <> '';                  -- de-duplicates chathistory/bouncer replays

-- Full-text search over message text, kept in sync by triggers.
CREATE VIRTUAL TABLE messages_fts USING fts5(
  text, content='messages', content_rowid='id', tokenize='trigram'
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
  PRIMARY KEY (network, buffer)     -- buffer is the folded name
);

CREATE TABLE prefs (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL              -- opaque JSON owned by the server
);
```

Pragmas: `journal_mode=WAL` (concurrent reads alongside the writer),
`synchronous=NORMAL` (the recommended WAL setting: a commit no longer waits for
an fsync, and a power loss can only drop the last few transactions, never
corrupt the file), `busy_timeout=5000`, `foreign_keys=1`.

**Migrations.** The full-table rewrites (backfilling `buffer_fold`,
de-duplicating rows by msgid, and swapping the old unfolded-buffer indexes for
the folded ones) are gated on `PRAGMA user_version` (currently `2`), so they
run once per database rather than on every start.

## The write path

Writes never run on the engine loop. `Print` and `Redact` enqueue an op onto a
bounded queue (4096 ops) drained by a single **writer goroutine**, which takes
everything already waiting (up to 1024 ops) and commits it in **one
transaction** — group commit, so a join burst or netsplit costs one commit, not
thousands. If a batch fails, it is retried row by row so one bad row never loses
the rest. `Print` only blocks when the disk falls 4096 writes behind.

Readers that must see their own just-queued lines (`MissedHighlights`,
`Prune`, backup) call an internal `flush()` barrier first. `Close` drains the
queue before closing the database.

## Message history

- **`Print(m core.Message)`** — the `Sink` write path. Queues a row (see
  above), stamping `ts` from `m.Time` (or now), `buffer_fold` from
  `core.FoldIRC(m.Buffer)`, and serializing `m.Tags` to JSON. `INSERT OR IGNORE`
  plus the msgid index means a line replayed by chathistory or a bouncer is not
  stored twice. The FTS index follows via trigger.
- **`Redact(...)`** — the `Sink` redaction path. Deletes the row by
  `(network, buffer_fold, msgid)`, queued behind any pending insert of the same
  line, so a redacted message stays gone after a reload and from search.
- **`Backlog(ctx, network, buffer, before, limit)`** — newest-first page ending
  before `before` (or the most recent page when zero). Fetches `limit+1` rows
  to compute the `more` flag, then reverses to oldest-first for the client.
- **`BacklogAround(ctx, network, buffer, around, limit)`** — a window of
  `limit/2` older + `limit/2` newer messages around an anchor time, returned
  oldest-first, with separate flags for history before and after the window.
  Backs jump-to-message and search-result navigation.
- **`Search(ctx, query, network, buffer, limit)`** — a case-insensitive,
  substring-capable FTS5 trigram MATCH over `messages_fts`, newest matches
  first, optionally scoped to a network/buffer. One- and two-character terms
  use a substring scan because they are shorter than a trigram.

- **`MissedHighlights(ctx, limit)`** — highlighted conversational lines newer
  than each buffer's read marker (newest `limit`, returned in arrival order).
  Backs the "What you missed" digest (`missed:fetch` → `missed:result`).
- **`Prune(ctx, before)`** — deletes messages older than `before` in batches of
  2000 rows with a short pause in between, so live inserts are never stuck
  behind a retention sweep of millions of rows. Driven hourly by
  `retention_days` (see [config.md](config.md)).

These feed the server's `backlog`, `context`, `search:result`, and
`missed:result` frames (see [protocol.md](protocol.md)).

## Read markers (unread that survives a reload)

The browser's unread badge is a live, in-memory counter — it would reset to
zero on every page load. `read_markers` makes it durable: one timestamp per
buffer recording how far the user has read.

- **`MarkRead(ctx, network, buffer, ts)`** — upsert the marker to `ts` (zero =
  now), `MAX`-merged so it only ever moves forward; a stale frame can't un-read
  a buffer. Driven by the c2s `read` frame, SSH TUI buffer switches, or IRC bouncer
  `MARKREAD` commands.
- **`ReadMarkers(ctx)`** — returns a map of all buffer read marker timestamps in
  unix milliseconds, delivered in the `init` snapshot (`InitState.read_markers`) so
  clients render the unread divider in the exact right spot across reloads and devices.
- **`UnreadCounts(ctx)`** — per buffer, how many conversational
  (`privmsg`/`notice`/`action`), non-self messages arrived after its marker,
  and how many of those are highlights. Only buffers that *have* a marker are
  reported, so a buffer's existing history is never retroactively counted as
  unread before it's been opened once. The server calls this at connect time
  and folds the counts into the `init` snapshot's `ChannelDTO.unread/highlight`.

## Server preferences & drafts (`prefs`)

The `prefs` table stores opaque JSON key/value preferences per user:
- `highlight` — user's highlight regex patterns and exception rules.
- `aliases` — user's custom `/command` alias expansions.
- `muted` — set of muted buffer identifiers.
- `settings` — UI settings (theme, font size, link preview expansion, colored nicks, typing indicators).
- `drafts` — unsent composer drafts per `(network, buffer)`, synced real-time across tabs and devices.
- `bouncer` — `"off"` when this account refuses native IRC clients (see [ircserver.md](ircserver.md#per-account-access-web-ui)); absent means allowed.

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

## Data Portability, Backups & Imports

stugan provides full database and configuration backup and restore functionality via `internal/backup` and the HTTP endpoints `/api/export` and `/api/import`:

- **Backup Snapshots (`Store.Backup`)**:
  Uses SQLite's `VACUUM INTO 'dest.db'` to take a consistent, compacted snapshot of the user's database without blocking concurrent readers or writers.
- **Archive Formats**:
  Backups are packaged into portable archives containing:
  - `manifest.json`: backup version, export timestamp, username, and metadata.
  - `stugan.db`: the SQLite database snapshot.
  - `scripts/`: custom Lua plugins and configs.
  Supported formats: Compressed Archive (`.tar.gz`), Zip Archive (`.zip`), or raw SQLite (`.db`).
- **Database Import & Restore (`Store.Import`)**:
  - **`replace` mode**: Atomically replaces messages, networks, read markers, plugin KV, and preferences with the imported database, and rebuilds the FTS5 search index.
  - **`merge` mode**: Performs a safe merge combining messages (`INSERT OR IGNORE`), networks (`INSERT OR REPLACE`), read markers (updating to `MAX(ts)`), plugin KV, and preferences.
  - The imported database must pass `PRAGMA quick_check`. Message rows are copied **by column name**, not position, so backups from older builds (which added `highlight`/`buffer_fold` later and in a different column order) import correctly; missing columns get defaults and `buffer_fold` is backfilled.

## Notes

- Tests use a temp on-disk database and exercise the backlog/search paths
  table-driven under `-race`.
</content>
