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
| `backlog:fetch` | history `Backlog` / `BacklogAround` |
| `search` | history `Search` (FTS5) |
| `net:add` | `AddNetworkLive` |
| `net:edit` | `UpdateNetwork` |
| `net:remove` | `RemoveNetwork` |
| `net:connect` | `SetConnected` |
| `net:info` | replies `net:info` with `NetworkConfig` |
| `typing` / `react` / `redact` | `SendTyping` / `SendReaction` / `SendRedact` |
| `list` | `ListChannels` |

## s2c fan-out — `userSink`

Each user has a `userSink` registered on their engine. It implements
`core.Sink` by marshaling committed events to wire frames and broadcasting them
to every browser attached to that user (`routeToUser`):

| Sink method | frame |
|-------------|-------|
| `Print` | `msg` (`MessageDTO`) |
| `NetworkChanged` | `net:update` (`NetworkDTO`) |
| `NetworkRemoved` | `net:remove` |
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
    Login(username, password string) (token string, ok bool)
    Session(token string) (userID string, ok bool)
    StartSession(userID string) (token string, maxAgeSec int)
    EndSession(token string)
    Tenant(userID string) (*Tenant, bool)   // *core.Engine + History
    Users() []string
}

type Tenant struct { Engine *core.Engine; History History }
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
- **SSRF guard.** `/api/preview` and `/api/proxy` use a guarded dialer that
  refuses private/loopback/link-local addresses, an 8s timeout, a redirect cap,
  and a response size cap (~10 MB for the proxy).
- **Upload safety.** Uploaded files get a random name + sanitized extension and
  are served with `X-Content-Type-Options: nosniff` and a `default-src 'none'`
  CSP so they can't execute as pages.

## Auxiliary services

- **Link previews (`previews.go`)** — fetch a URL, extract `og:title` /
  `og:description` / `og:image` (falling back to `<title>`/meta description),
  return JSON, cache for an hour.
- **Image/video proxy (`fetch.go`, `previews.go`)** — stream remote media back
  through the daemon to hide the client IP and avoid mixed-content warnings.
- **Uploads (`uploads.go`)** — multipart `POST /api/upload` → a served
  `/uploads/<random>.<ext>` URL. Each upload gets a sidecar record under
  `uploads/.meta/` (owner, original name, upload time; never served — the
  file server refuses dotted path segments) that backs the per-user
  `GET /api/uploads` listing. Files are retained between 3 and 7 days by
  size — `MIN_AGE + (MAX_AGE − MIN_AGE) × (1 − size/max_size)²`, so larger
  files are deleted earlier — enforced by an hourly sweep (plus one at
  startup) that also removes orphaned sidecars.
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
