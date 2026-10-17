# The server

`internal/server` is the HTTP + WebSocket front end. It serves the built
client, bridges each browser session to the right user's `core.Engine` over the
typed wire protocol ([protocol.md](protocol.md)), and provides the auxiliary
HTTP endpoints (auth, uploads, previews, push). It imports `core`, `proto`, and
`auth`, but never girc or Lua.

## Routes

| Route | Method | Purpose |
|-------|--------|---------|
| `/ws` | GET | WebSocket upgrade; resolves the user, sends `hello`+`init`, then streams frames |
| `/api/login` | POST | username/password → sets `stugan_session` cookie |
| `/api/logout` | POST | invalidates the session |
| `/api/me` | GET | reports auth state (enabled, authenticated, user, magic-word grant) |
| `/api/magicword` | POST | site-wide password gate → sets `stugan_magic` cookie |
| `/api/magicword/logout` | POST | clears the magic cookie |
| `/api/export` | GET | download a backup of the user's data; `?format=tar.gz` (default) \| `zip` \| `db` |
| `/api/import` | POST | restore a backup (multipart `file`, or a raw body); `?mode=replace` (default) \| `merge`; 500 MB cap |
| `/api/preview` | GET | fetch a URL and return Open Graph metadata |
| `/api/proxy` | GET | proxy a remote image/video (hides client IP, avoids mixed content) |
| `/api/upload` | POST | multipart upload → stored file URL + expiry |
| `/api/uploads` | GET | list the requesting user's stored uploads (url, name, size, uploaded, expires) |
| `/uploads/*` | GET | serve uploaded files (with `nosniff` + restrictive CSP) |
| `/api/push/vapid` | GET | VAPID public key for push subscription |
| `/api/push/subscribe` | POST | store a browser push subscription |
| `/healthz` | GET | health check |
| `/` | GET | static SPA assets (`client/dist`, configurable) |

## c2s routing

`route()` switches on `Envelope.T` and calls into the user's engine:

| `t` | engine call |
|-----|-------------|
| `msg:send` | `SendInput` (alias → plugin command → built-in → IRC) |
| `backlog:fetch` | history `Backlog` / `BacklogAround` (async) |
| `context:fetch` | history window around one message (async) |
| `search` | history `Search` (FTS5, async) |
| `missed:fetch` | history `MissedHighlights` (async) |
| `read` | `MarkRead` (persists, fans out via `Sink.ReadMarker`) |
| `complete:req` | `Complete` → plugin `hook_completion` (async) |
| `net:add` | `AddNetworkLive` |
| `net:edit` | `UpdateNetwork` (merged over the existing `NetworkConfig`) |
| `net:remove` | `RemoveNetwork` |
| `net:connect` | `SetConnected` |
| `net:info` | replies `net:info` with `NetworkConfig` |
| `net:reorder` / `buf:reorder` | `ReorderNetworks` / `ReorderBuffers` |
| `buf:close` | `CloseBuffer` |
| `monitor:add` / `monitor:remove` | `AddMonitor` / `RemoveMonitor` |
| `typing` / `react` / `redact` | `SendTyping` / `SendReaction` / `SendRedact` |
| `list` | `ListChannels` |
| `plugin:list` | replies `plugin:list` (`Plugins` + `CuratedPlugins`) |
| `plugin:action` | load/unload/uninstall/reload/import/download/update/check_updates (async) |
| `plugin:setting` | `SetPluginSetting`, then replies `plugin:list` |
| `highlight:set` / `aliases:set` | `SetHighlighter` / `SetAliases` + `prefs`, broadcast to the user's tabs |
| `settings:set` / `draft:set` / `mute` | `prefs` only, broadcast to the user's tabs (`draft` skips the sender) |
| `bouncer:set` | `prefs` `bouncer`; denying drops attached IRC clients; broadcasts `bouncer` |
| `presence` | records tab visibility (push suppression) |
| `ping` | replies `pong` |

"Async" handlers run off the read loop (see WebSocket limits below).

## s2c fan-out — `userSink`

Each user has a `userSink` registered on their engine. It implements
`core.Sink` by marshaling committed events to wire frames and broadcasting them
to every browser attached to that user (`routeToUser`):

| Sink method | frame |
|-------------|-------|
| `Print` | `msg` (`MessageDTO`) |
| `NetworkChanged` | `net:update` (`NetworkDTO`) |
| `NetworkRemoved` | `net:remove` |
| `NetworksReordered` | `net:reorder` |
| `ReadMarker` | `read` |
| `Typing` | `typing` (with `nick`) |
| `React` | `react` (with `nick`) |
| `Redact` | `redact` (with `by`) |
| `ChannelList` | `list:result` |

On connect, the same sink replays a `hello` + a full `init` snapshot so the
client starts from authoritative state.

## Multi-tenancy — the Hub

The `server.Hub` is the seam between a connection and a user's engine:

```go
type Hub interface {
    AuthEnabled() bool
    Login(username, password string) (userID string, ok bool)
    Session(token string) (userID string, ok bool)
    StartSession(userID string) (token string, maxAgeSec int)
    EndSession(token string)
    Tenant(userID string) (*Tenant, bool)
    Users() []string
}

type Tenant struct {
    Engine     *core.Engine
    History    History      // backlog / search / missed / read markers
    Prefs      Prefs        // the store's prefs table
    Store      BackupStore  // Backup / Import for /api/export, /api/import
    ScriptsDir string       // restored into on import
}
```

A `/ws` connection resolves its user from the `stugan_session` cookie, or — when
auth is disabled — the implicit `default` user. The concrete hub is built in
`cmd/stugan` over the per-user engines/stores.

## Authentication (`internal/auth`)

- **`Users`** — a `username → bcrypt hash` map. `Verify` is constant-time and
  hashes against a dummy entry for unknown usernames to avoid a timing
  side-channel.
- **`Sessions`** — opaque random tokens (32 bytes, base64) with a TTL.
  `Create` / `Lookup` / `Delete`.
- **Cookies** — `stugan_session` (per-user) and `stugan_magic` (site gate),
  both `HttpOnly`, `SameSite=Strict`, `Secure` under TLS. Session lifetime is
  configurable (`[auth] session_hours`, default 30 days).

Generate a password hash with `stugan -hashpw` (reads stdin, prints a bcrypt
hash for a `[[users]]` `password_hash`).

## Security hardening

- **Site-wide password gate.** Setting `STUGAN_WEB_PASSWORD` puts a
  single-shared-password prompt in front of `/ws`, `/api/*`, and `/uploads/*`.
  The password is bcrypt-hashed in memory at startup (plaintext never
  retained); a grant lives in `stugan_magic` for 30 days. Stacks with
  `[[users]]` (magic word first, then per-user login).
- **Rate limiting.** `/api/login` and `/api/magicword` are limited per source
  IP (a sliding window, ~8 fails/minute); failures answer after a short delay.
  The login/magic-word forms carry honeypot inputs that trip form-filling bots.
  The source IP is the direct peer (`RemoteAddr`) unless that peer is listed in
  `server.trusted_proxies`, in which case the real client is taken from
  `CF-Connecting-IP` / `X-Forwarded-For` — set this behind any reverse proxy
  (incl. Cloudflare Tunnel) so the limiter keys per visitor rather than
  collapsing every request onto the proxy's address. Untrusted peers' forwarded
  headers are ignored, so they can't be spoofed to dodge the limit.
- **SSRF guard.** `/api/preview`, `/api/proxy`, and the Lua `stugan.http`
  binding all fetch through `internal/safehttp`. Its client resolves the host
  and dials only globally routable addresses — refusing private, loopback,
  link-local, unspecified and multicast addresses plus ranges the stdlib
  predicates miss (`100.64.0.0/10` CGNAT, `192.0.0.0/24`, `198.18.0.0/15`,
  `240.0.0.0/4`) — with an 8s timeout and at most 5 redirects. The proxy also
  caps responses at ~10 MB. Because the check happens at dial time, a DNS
  answer can't swap in an internal address after validation.
- **WebSocket limits.** One inbound frame is capped at 1 MiB. Slow handlers
  (database queries, plugin downloads, completion hooks) run off the
  connection's read loop — up to 4 in flight per socket, then inline, which
  throttles a client flooding slow requests — so a slow query can't starve the
  pong and get a healthy socket killed.
- **Upload safety.** Uploaded files get a random name + sanitized extension and
  are served with `X-Content-Type-Options: nosniff` and a `default-src 'none'`
  CSP so they can't execute as pages.

## Auxiliary services

- **Backup & restore (`backup.go`)** — `GET /api/export` snapshots the user's
  database (`VACUUM INTO`) and packages it with their `scripts/` and a
  manifest via `internal/backup`. `POST /api/import` accepts the same
  archive (or a bare `.db`), imports it in `replace` or `merge` mode
  ([storage.md](storage.md#data-portability-backups--imports)), copies any
  bundled scripts back into the user's scripts dir, and re-syncs every
  attached browser with a fresh `init`.
- **Link previews (`previews.go`)** — fetch a URL, extract `og:title` /
  `og:description` / `og:image` (falling back to `<title>`/meta description),
  return JSON, cache for an hour.
- **Image/video proxy (`fetch.go`, `previews.go`)** — stream remote media back
  through the daemon to hide the client IP and avoid mixed-content warnings.
- **Uploads (`uploads.go`)** — multipart `POST /api/upload` → a served
  file URL. Supports `local` mode (files saved under `uploads/` with random names)
  or `custom` mode (files posted to external upload services like `x0.at` or `0x0.st`).
  Image files are stripped of EXIF/location metadata prior to storage/upload.
  Each upload gets a sidecar record under `uploads/.meta/` (owner, original name,
  upload time, size, external URL if custom; never served) backing `GET /api/uploads`.
  Local files and sidecars are retained between 3 and 7 days by size
  (`MIN_AGE + (MAX_AGE − MIN_AGE) × (1 − size/max_size)²`), enforced by an hourly sweep.
- **Web Push (`push.go`)** — a VAPID keypair (persisted under the data dir) and
  per-user subscriptions. When a highlight arrives for a user with no attached
  browser, a push is sent; dead subscriptions are pruned on 404/410.

## Verifying changes

There is no mock IRC server. The established way to verify end-to-end behavior
is to run the daemon against Libera (`irc.libera.chat:6697`, TLS) with a random
nick in a low-traffic channel, then drive it with a throwaway Node WebSocket
client (`ws://127.0.0.1:8080/ws`) that sends/reads `proto` frames. The package
tests themselves use `httptest` + `coder/websocket` Dial against an in-process
engine with a `fakeConn`.
</content>
