# Running with Docker

The `.github/workflows/docker.yml` GitHub Action builds a multi-arch image
(`linux/amd64` + `linux/arm64`) and publishes it to the GitHub Container
Registry on every push to `main` and every `v*` tag. This guide covers pulling
that image onto a server and running it.

## The image

```
ghcr.io/klppl/stugan
```

Tags produced by the workflow:

| Tag | When |
|-----|------|
| `latest` | every push to the default branch (`main`) |
| `main` | same builds, branch-named |
| `v1.2.3`, `1.2` | when you push a `v*` git tag (semver) |
| `sha-<short>` | every build, immutable — pin to this for reproducible deploys |

To cut a versioned release, tag a commit and push it:

```sh
git tag v0.1.0 && git push origin v0.1.0
```

### Make the package pullable

GHCR packages are **private by default**. For an unauthenticated
`docker pull` on your VPS, open the package on GitHub
(`github.com/users/klppl/packages/container/stugan/settings`) and set its
visibility to **Public**. To keep it private instead, log in on the VPS with a
Personal Access Token that has `read:packages`:

```sh
echo "$GHCR_PAT" | docker login ghcr.io -u klppl --password-stdin
```

## Quick run

```sh
docker run -d --name stugan \
  -p 8080:8080 \
  -v stugan-data:/data \
  --restart unless-stopped \
  ghcr.io/klppl/stugan:latest
```

- `-v stugan-data:/data` — config, the SQLite history, scripts, and uploads all
  live in `/data` (the image sets `STUGAN_HOME=/data`). Use a named volume (as
  above) or a bind mount.
- `--restart unless-stopped` — bring it back after a reboot.
- The container runs as a non-root user (uid `10001`); a named volume gets the
  right ownership automatically. For a **bind mount**, `chown -R 10001:10001`
  the host directory first.

## You must set `listen = "0.0.0.0:8080"`

stugan defaults to `listen = "127.0.0.1:8080"`, which inside a container is
**not reachable from the host or the published port**. Create a `config.toml`
in the data volume with a `0.0.0.0` bind before exposing it.

Write the config into the named volume (one-off helper container):

```sh
docker run --rm -v stugan-data:/data alpine sh -c 'cat > /data/config.toml' <<'TOML'
[server]
listen     = "0.0.0.0:8080"
public_url = "https://irc.example.com"        # used for push + absolute links
origin_patterns = ["irc.example.com"]          # WebSocket Origin allowlist

[[networks]]
name     = "libera"
addr     = "irc.libera.chat:6697"
tls      = true
nick     = "yournick"
channels = ["#stugan"]
TOML
docker restart stugan
```

`public_url` and `origin_patterns` matter once you serve from a real hostname
behind a proxy (below). See [config.md](config.md) for every field. After the
first run the SQLite store is authoritative for networks — manage them from the
web UI, not by editing `config.toml`.

## docker-compose

```yaml
services:
  stugan:
    image: ghcr.io/klppl/stugan:latest
    container_name: stugan
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - stugan-data:/data
    # Optional site-wide password gate (see below). Use an env_file or a secret
    # for the real value rather than committing it.
    # environment:
    #   STUGAN_WEB_PASSWORD: ${STUGAN_WEB_PASSWORD}

volumes:
  stugan-data:
```

```sh
docker compose up -d
docker compose logs -f          # watch it connect
```

## Behind a reverse proxy (TLS)

Terminate TLS at a proxy and forward to the container. The app uses a
WebSocket at `/ws`, so the proxy **must forward Upgrade/Connection headers**.
Set `server.public_url` to your `https://` URL and add the host to
`origin_patterns`.

Caddy (handles certs and WebSocket upgrades automatically):

```
irc.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

nginx:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
}
```

With a proxy on the same host you can drop the public port mapping and bind the
container to localhost instead: `-p 127.0.0.1:8080:8080`.

### Set `trusted_proxies` so login throttling works

Behind any proxy the daemon sees every request coming from the proxy's address,
not the visitor's. The auth rate-limiter keys on that address, so without
configuration a handful of failed logins from *anyone* throttles the login
endpoints for *everyone*. Tell stugan which peers are proxies so it reads the
real client IP from `CF-Connecting-IP` / `X-Forwarded-For` instead:

```toml
[server]
trusted_proxies = ["127.0.0.1/32", "::1/128"]   # same-host proxy / cloudflared
```

- **Cloudflare Tunnel** (`cloudflared` → the container): the peer is loopback,
  so `["127.0.0.1/32", "::1/128"]` is exactly right; the visitor IP comes from
  `CF-Connecting-IP`.
- **Same-host nginx/Caddy**: same loopback values.
- **Cloudflare proxying straight to a public origin** (no local proxy): set this
  to Cloudflare's [published IP ranges](https://www.cloudflare.com/ips/) instead.

It is **not** an environment variable — like all non-secret settings it lives in
`config.toml` in the `/data` volume. Forwarded headers are ignored from any peer
not listed here, so an attacker hitting the origin directly can't spoof them.

## Authentication

**Site-wide password gate** — a single shared password in front of everything,
useful before you set up accounts. Set `STUGAN_WEB_PASSWORD` (hashed in memory
at startup; the plaintext is never stored):

```sh
docker run -d --name stugan -p 8080:8080 -v stugan-data:/data \
  -e STUGAN_WEB_PASSWORD='hunter2' ghcr.io/klppl/stugan:latest
```

**Multi-user accounts** — add `[[users]]` blocks to `config.toml`. Generate a
bcrypt `password_hash` with the same image:

```sh
docker run --rm -i ghcr.io/klppl/stugan:latest -hashpw
# type the password, press Enter — paste the printed hash into config.toml
```

See [config.md](config.md#multi-user--auth-and-users) for the block shape and
[server.md](server.md#authentication-internalauth) for how sessions/cookies work.

## Updating

```sh
docker pull ghcr.io/klppl/stugan:latest
docker rm -f stugan
# re-run the same `docker run …`, or:  docker compose up -d
# (compose: `docker compose pull && docker compose up -d`)
```

Your `/data` volume — history, networks, scripts, config — survives across
upgrades. For predictable deploys, pin a `sha-<short>` or `v*` tag instead of
`latest`.

## Building locally

The same `Dockerfile` builds without CI (it compiles the Vue client and a
static, CGO-free Go binary in multi-stage):

```sh
docker build -t stugan .
docker run -d -p 8080:8080 -v stugan-data:/data stugan
```
</content>
