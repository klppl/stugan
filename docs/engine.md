# stugan-engine: IRC that survives restarts

Updating stugan normally means a disconnect/reconnect flood: every network sees
you quit and rejoin, services re-identify you, and anything said while stugan
was restarting is gone. **stugan-engine** is an optional second process that
holds the IRC connections while stugan itself restarts. Upgrade stugan, and
your connections stay up: same nick, same channels, nothing re-registers, and
what was said meanwhile is delivered (with its real timestamps) as soon as
stugan is back.

Without the engine, stugan dials IRC itself, exactly as before. Turning it on
is one setting.

---

## How it works

```
 browser ─┐                                   ┌─ irc.libera.chat
 SSH TUI ─┼─ stugan ══ one link (TCP/unix) ══ stugan-engine ─┼─ irc.oftc.net
 bouncer ─┘   (web UI, plugins, history)      (IRC sockets)  └─ …
```

- The engine is a **line relay**. It owns the TCP/TLS sockets and moves raw IRC
  lines both ways. It understands only a handful of commands: it answers
  `PING`, records the registration burst, and tracks your nick, channels and
  negotiated capabilities. Everything else (IRCv3, history, plugins, the UI)
  stays in stugan.
- When stugan stops, it **detaches**. The engine keeps every socket registered,
  answers the server's PINGs, and buffers what arrives.
- When stugan starts, it asks the engine to *connect-or-attach* each network.
  For a socket the engine holds, it gets a replay of the registration burst, so
  its fresh IRC client believes it just registered, then every line it hasn't
  stored yet. The IRC server sees nothing at all.
- stugan **acknowledges** lines only once its engine loop has committed them,
  and the engine forgets only acknowledged lines. Across a clean restart each
  message is stored exactly once: nothing is lost and nothing is duplicated.

Because the engine knows so little IRC, it rarely needs to change, and so
rarely needs restarting. Restarting the engine itself is the one thing that
still drops IRC.

---

## Docker (recommended)

You add **one service** (`stugan-engine`) and **two environment variables** on
`stugan` to the stack you already have. Pick the case that matches you:

| You run stugan today with… | Do this |
|----------------------------|---------|
| Nothing yet (new install) | [Use `docker-compose.yml`](#new-install) |
| The compose file from [docker.md](docker.md) | [Add the engine to it](#existing-compose-file) |
| Your own compose behind a tunnel/proxy network (Dockge, Portainer, …) | [Same, with a private network](#behind-a-reverse-proxy-or-tunnel-network) |
| `docker run` | [Move to compose, keeping your volume](#coming-from-docker-run) |

All cases share two rules:

- **Keep your data where it is.** Leave the `/data` volume or bind mount line
  exactly as it is today. See [Keep your data](#keep-your-data) before you
  switch files or directories.
- **Generate a secret** and put it in the stack's `.env` (Dockge and Portainer
  have an editor for it): `STUGAN_ENGINE_SECRET=` followed by the output of
  `openssl rand -hex 32`.

### New install

The repository ships [`docker-compose.yml`](../docker-compose.yml); the engine
is its `engine` profile:

```sh
cat > .env <<EOF
COMPOSE_PROFILES=engine
STUGAN_ENGINE_URL=tcp://stugan-engine:8016
STUGAN_ENGINE_SECRET=$(openssl rand -hex 32)
EOF
docker compose up -d
```

| Container | Image | Holds |
|-----------|-------|-------|
| `stugan` | `ghcr.io/klppl/stugan:latest` | web UI, history (`/data`), plugins |
| `stugan-engine` | `ghcr.io/klppl/stugan:engine-1` | the IRC sockets (no volume) |

stugan still needs a `config.toml` with `listen = "0.0.0.0:8080"` in its data
volume (see [docker.md](docker.md#you-must-set-listen--00008080)).

### Existing compose file

If you started from the compose in [docker.md](docker.md), **edit that file**
rather than switching to a new one. The lines marked `# new` are all that
changes:

```yaml
services:
  stugan:
    image: ghcr.io/klppl/stugan:latest
    container_name: stugan
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - stugan-data:/data                               # unchanged
    environment:
      STUGAN_ENGINE_URL: tcp://stugan-engine:8016        # new
      STUGAN_ENGINE_SECRET: ${STUGAN_ENGINE_SECRET}      # new
    depends_on:                                          # new
      stugan-engine:                                     # new
        condition: service_healthy                       # new

  stugan-engine:                                         # new: the whole service
    image: ghcr.io/klppl/stugan:engine-1
    container_name: stugan-engine
    restart: unless-stopped
    environment:
      STUGAN_ENGINE_SECRET: ${STUGAN_ENGINE_SECRET}
      STUGAN_ENGINE_LISTEN: 0.0.0.0:8016
    healthcheck:
      test: ["CMD", "/app/stugan-engine", "-health"]
      interval: 10s
      timeout: 3s
      retries: 3
      start_period: 5s

volumes:
  stugan-data:
```

Both services sit on the stack's default network, so stugan reaches the engine
as `stugan-engine`. Don't add `ports:` to the engine: it must never be
reachable from outside.

### Behind a reverse proxy or tunnel network

If stugan joins an external network shared with a reverse proxy or tunnel
(here `cloudflared_net`), give the two containers a private network of their
own and keep the engine off the shared one, so nothing else can reach it:

```yaml
services:
  stugan:
    image: ghcr.io/klppl/stugan:latest
    restart: unless-stopped
    networks: [cloudflared_net, engine]   # proxy network + private link
    volumes:
      - ./data:/data                      # unchanged: your history and config
    environment:
      - STUGAN_ENGINE_URL=tcp://stugan-engine:8016
      - STUGAN_ENGINE_SECRET=${STUGAN_ENGINE_SECRET}
    depends_on:
      stugan-engine:
        condition: service_healthy

  stugan-engine:
    image: ghcr.io/klppl/stugan:engine-1
    container_name: stugan-engine
    restart: unless-stopped
    networks: [engine]                    # never the shared proxy network
    environment:
      - STUGAN_ENGINE_SECRET=${STUGAN_ENGINE_SECRET}
      - STUGAN_ENGINE_LISTEN=0.0.0.0:8016
    healthcheck:
      test: ["CMD", "/app/stugan-engine", "-health"]
      interval: 10s
      timeout: 3s
      retries: 3
      start_period: 5s

networks:
  cloudflared_net:
    external: true
  engine: {}                              # not `internal: true`: the engine must reach IRC servers
```

Keep any other settings you already have on `stugan`, such as
`STUGAN_WEB_PASSWORD`, `security_opt` or a healthcheck.

### Coming from `docker run`

A volume created by `docker run -v stugan-data:/data` is called exactly
`stugan-data`. Compose would otherwise prefix it with the project name and
start you on an empty one, so declare it as external:

```yaml
volumes:
  stugan-data:
    external: true      # the volume your docker run used
```

Use that with the [existing compose file](#existing-compose-file) example, then
switch over:

```sh
docker rm -f stugan                 # the old docker-run container (the volume stays)
docker compose up -d
```

### Keep your data

- **Bind mounts** (`./data:/data`) are relative to the compose file. Keep the
  file in the same directory, or use an absolute path.
- **Named volumes** are namespaced by the compose project, which is the
  directory name unless `name:` or `-p` says otherwise. Keep using the same
  file in the same directory, or point at the volume by its real name with
  `external: true`. `docker volume ls` shows the real names (e.g.
  `stugan_stugan-data`).
- If stugan comes up with no networks and an empty history, it's looking at a
  new volume. Your old one is untouched: stop the stack and fix the volume
  line.

### Deploy and check

1. `docker compose pull` (or **Update** in Dockge/Portainer) and
   `docker compose up -d`. This first deploy drops IRC once, while the
   connections move into the engine.
2. stugan's log should say `IRC connections go through stugan-engine` and
   `connected to stugan-engine`.
3. Restart only the `stugan` container. Its status buffer should then say
   `resumed connection held by stugan-engine (detached …)`, and nobody in your
   channels sees you quit.

From then on, the usual `pull` + `up -d` (or **Update**) recreates only
`stugan`. The engine's image hasn't changed, so it keeps running.

> **Why `engine-1` and not `latest`?** Compose recreates every service whose
> image changed. On `latest`, each stugan release would restart the engine too
> and drop IRC, which is the thing it exists to prevent. The `engine-1` tag
> moves only when the engine itself changes (the changelog says so). The `1`
> is the protocol major version: a stugan that needs `engine-2` will say so,
> and refuses to talk to an `engine-1` rather than guess.

In a container the engine must listen on `0.0.0.0`
(`STUGAN_ENGINE_LISTEN=0.0.0.0:8016`): containers don't share loopback. With
no published port, `:8016` exists only on the stack's network.

If a pull fails with `unauthorized`, the GHCR package is private: make it
public or `docker login ghcr.io` on the host (see
[docker.md](docker.md#make-the-package-pullable)).

### Opting in or out

- **Opting in** drops IRC once: the `up -d` that adds the engine restarts
  stugan with the new setting, and the connections move into the engine.
- **Opting out**: stop the engine *first*, then bring stugan up without
  `STUGAN_ENGINE_URL`. Otherwise stugan dials IRC itself while the engine still
  holds the old sockets, and your nick collides with its own ghost (the engine
  reaps unclaimed sockets after an hour, but that is a backstop).

### Upgrading the engine

When a release notes an engine change, the `engine-1` tag moves and the next
`up -d` restarts the engine. That drops IRC once, and stugan reconnects
normally. For a new protocol major (`engine-2`), stop the engine first, then
upgrade both.

---

## Without Docker

Build and run both binaries:

```sh
go build -o stugan ./cmd/stugan
go build -o stugan-engine ./cmd/stugan-engine

export STUGAN_ENGINE_SECRET=$(openssl rand -hex 32)
./stugan-engine &                                   # listens on 127.0.0.1:8016
STUGAN_ENGINE_URL=127.0.0.1:8016 ./stugan
```

With systemd, run them as two units (`stugan-engine.service`,
`stugan.service`) so that `systemctl restart stugan` leaves the engine alone.
A unix socket keeps the link off the network entirely:

```sh
STUGAN_ENGINE_LISTEN=unix:///run/stugan/engine.sock ./stugan-engine
STUGAN_ENGINE_URL=unix:///run/stugan/engine.sock ./stugan
```

The socket is created mode `0660`; the secret is still checked on top.

---

## Configuration

### stugan

Either in `config.toml`:

```toml
[engine]
url    = "tcp://stugan-engine:8016"   # host:port, tcp://host:port or unix:///path
secret = "…"                          # at least 16 characters
```

or through the environment, which overrides the file: `STUGAN_ENGINE_URL`,
`STUGAN_ENGINE_SECRET`. An empty URL disables the engine.

stugan also keeps an **instance id** in `data/engine-instance`. The engine
partitions connections by it, so two stugan installations pointed at one
engine can never reach each other's sockets. Deleting the file makes the next
start look like a different installation: it dials fresh, and the old sockets
are reaped.

### stugan-engine

Configured from the environment (flags override `-listen`):

| Variable | Default | Meaning |
|----------|---------|---------|
| `STUGAN_ENGINE_SECRET` | *(required)* | Shared secret, at least 16 characters. `openssl rand -hex 32` makes a good one. |
| `STUGAN_ENGINE_LISTEN` | `127.0.0.1:8016` | `host:port`, `tcp://host:port` or `unix:///path`. Use `0.0.0.0:8016` in a container. |
| `STUGAN_ENGINE_BUFFER_BYTES` | `4194304` (4 MiB) | Per-connection cap on lines buffered while stugan is away. |
| `STUGAN_ENGINE_BUFFER_TOTAL_BYTES` | `268435456` (256 MiB) | Cap across all connections; under pressure the largest backlog sheds first. |
| `STUGAN_ENGINE_ORPHAN_TIMEOUT` | `1h` | End a connection no stugan has claimed for this long (Go duration). `0` keeps them forever. |
| `STUGAN_ENGINE_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `STUGAN_ENGINE_LOG_FORMAT` | `text` | `text` or `json`. |

`stugan-engine -health` probes a running engine at `-listen` and exits 0 if it
answers. The Docker healthcheck uses it. `stugan-engine -version` prints the
version.

---

## What you will see

After a restart, each network's status buffer shows:

```
connecting to irc.libera.chat:6697 (TLS) via stugan-engine…
resumed connection held by stugan-engine (detached 12s, 34 buffered line(s))
resumed Libera as yournick
```

Messages that arrived while stugan was down appear in their buffers with the
time they actually arrived. If stugan stayed away long enough for the engine's
buffer to fill, the oldest lines were dropped and the status buffer says how
many:

```
warning: stugan-engine's buffer filled while stugan was down; 1200 earlier line(s) were lost
```

## Behaviour details

- **Perform and autojoin don't re-run** on a resumed connection. It never left
  its channels and stayed identified. The plugin signal is `resume` instead of
  `connect`, so scripts that identify or join on `connect` (nickserv, qauth,
  away) don't repeat themselves (see [plugins.md](plugins.md)).
- **Members and topics are refreshed.** A new stugan process has no member
  lists or topics in memory, so it sends `NAMES` and `TOPIC` for each channel
  after resuming, at low priority behind anything you type. The replies don't
  print in the buffers.
- **Edits force a fresh connection.** The engine re-attaches only when the
  registration settings match: address, fallbacks, TLS, skip-verify, username,
  realname, SASL, server password, client certificate. Changing any of these
  ends the held socket and dials anew, just as it reconnects without the
  engine. A **nick** change does not, since it applies live.
- **Disconnect, remove and edit** end the IRC connection as before. Only
  stugan's own shutdown detaches.
- **Rolling deploys.** When two stugan processes overlap, the newer one takes
  the connections, and the older one is refused rather than allowed to take
  them back.
- **Dead sockets are noticed while detached.** An IRC socket that goes silent
  for 90s is probed with a PING and ended if the probe goes unanswered for 60s,
  so stugan never re-attaches to a dead connection.
- **Registration in progress.** A socket that hadn't finished registering when
  stugan went away is not re-attached: stugan dials fresh instead. One that
  finished registering *while* stugan was away is resumed with perform and
  autojoin run, because they never ran.

## Limits

- **Restarting the engine drops IRC.** That's why its image tag moves rarely.
- The buffer is in memory, bounded by the caps above. The engine is a bridge
  across restarts, not a second message log.
- **STS** (automatic upgrade of a plaintext connection to TLS) isn't performed
  through the engine. Configure TLS on the network explicitly.
- While stugan is away, nobody answers CTCP requests, and a server that closes
  the connection takes the not-yet-delivered lines with it.
- Lines that aren't valid IRC (no command, an empty prefix) are dropped by the
  engine. Relayed, they would make stugan reconnect over and over.

## Security

- The link carries everything an IRC connection does, including SASL
  passwords and client certificate keys. Keep it on loopback, a unix socket or
  a private container network, and **never publish the engine's port**. The
  link itself isn't encrypted.
- The secret is compared in constant time. The engine never logs line contents
  or certificates.

## Troubleshooting

| Status message | Meaning |
|----------------|---------|
| `stugan-engine unreachable at …` | The engine isn't running or `STUGAN_ENGINE_URL` is wrong. stugan retries with backoff. |
| `stugan-engine refused the link: authentication failed: wrong engine secret` | `STUGAN_ENGINE_SECRET` differs between the two. |
| `stugan-engine refused the link: protocol mismatch …` | The engine image is a different protocol major. See [Upgrading the engine](#upgrading-the-engine). |
| `connection is held by a newer stugan process` | Another, newer stugan process owns this connection (an overlapping deploy). The older one keeps retrying until it's stopped. |
| `lost connection to stugan-engine` | The link dropped. If the engine is still up, the reconnect re-attaches. If it restarted, it dials fresh. |

## Internals

- Wire protocol: newline-delimited JSON frames, documented in
  [`internal/engineproto`](../internal/engineproto/proto.go). Lines that aren't
  valid UTF-8 travel base64-encoded, so the relay is byte-exact.
- Engine: [`internal/engine`](../internal/engine/), covering sessions, the
  replay, the byte-bounded buffers and the reaper.
- stugan side: [`internal/enginelink`](../internal/enginelink/) (the link and a
  `net.Conn` session that girc dials through) and
  [`internal/irc/relay.go`](../internal/irc/relay.go) (attach, restore, acks).
  See [irc.md](irc.md#stugan-engine-relay) and [core.md](core.md).
