# stugan-previews: isolated link previews

When someone posts a URL, stugan fetches the page to show a preview card, and
streams remote images and video through `/api/proxy`. That means a server
you've never heard of chooses what your daemon connects to and parses. By
default this happens inside the main stugan process, behind the SSRF guard in
`internal/safehttp`.

**stugan-previews** is an optional sidecar that does that work instead. It
holds no history, no IRC credentials and no sessions, so a bug in an HTML
parser or a fetcher can't reach them, and you can put it on a network with no
route to your LAN. It is also where site-specific previews live (YouTube,
Instagram, …), so the riskier fetching is never in the main process.

Without it, previews work exactly as before. Turning it on is one setting.

```
 browser ── stugan ── HTTP + shared secret ── stugan-previews ── the internet
          (history,                          (no data, no creds,
           credentials)                       SSRF-guarded fetcher)
```

stugan asks the sidecar for `/v1/preview?url=…` (a JSON card) and
`/v1/proxy?url=…` (image/video bytes). The sidecar caches cards for an hour.
**If the sidecar is down, previews fail; stugan does not silently fall back to
fetching in-process**, since that would defeat the isolation. Chat is
unaffected.

## Setup

### Docker

[`docker-compose.previews.yml`](../docker-compose.previews.yml) is a complete
two-container stack. Put the secret (and any provider keys) in `.env`:

```sh
echo "STUGAN_PREVIEWS_SECRET=$(openssl rand -hex 32)" > .env
docker compose -f docker-compose.previews.yml up -d
```

To add it to an existing compose file, add the service and two variables on
`stugan`:

```yaml
  stugan:
    environment:
      STUGAN_PREVIEWS_URL: http://stugan-previews:8017
      STUGAN_PREVIEWS_SECRET: ${STUGAN_PREVIEWS_SECRET}

  stugan-previews:
    image: ghcr.io/klppl/stugan:previews
    restart: unless-stopped
    environment:
      STUGAN_PREVIEWS_SECRET: ${STUGAN_PREVIEWS_SECRET}
      STUGAN_PREVIEWS_LISTEN: 0.0.0.0:8017
```

The sidecar is stateless, so (unlike [stugan-engine](engine.md)) it's fine for
it to update along with stugan. To harden it further, give it its own network
that only stugan shares, and no route to your LAN or other containers.

### Bare metal

```sh
go build -o stugan-previews ./cmd/stugan-previews
STUGAN_PREVIEWS_SECRET=$(openssl rand -hex 32) ./stugan-previews
```

and in stugan's `config.toml` (or the matching `STUGAN_PREVIEWS_URL` /
`STUGAN_PREVIEWS_SECRET` variables):

```toml
[previews]
url    = "127.0.0.1:8017"   # host:port or http(s)://host:port
secret = "…"                # ≥16 characters, same on both sides
```

## Sidecar environment

| Variable | Meaning |
|----------|---------|
| `STUGAN_PREVIEWS_SECRET` | Shared secret (required, ≥16 chars). stugan presents it as a Bearer token. |
| `STUGAN_PREVIEWS_LISTEN` | Listen address. Default `127.0.0.1:8017`; use `0.0.0.0:8017` in a container. |
| `STUGAN_PREVIEWS_LOG_LEVEL` | `debug`, `info` (default), `warn`, `error`. `debug` shows why a provider fell through. |
| `STUGAN_PREVIEWS_LOG_FORMAT` | `text` (default) or `json`. |
| `STUGAN_PREVIEWS_<PROVIDER>_<NAME>` | Provider settings, below. |

Provider settings and the secret also accept a `_FILE` form, e.g.
`STUGAN_PREVIEWS_YOUTUBE_API_KEY_FILE=/run/secrets/yt`, whose file contents are
used. That's the way to pass keys with Docker or Kubernetes secrets.

## Providers

A URL goes through the matching providers in order; the first to return a card
wins, and a failure falls through to the next, ending at the generic Open Graph
scraper. A site that can't be previewed therefore degrades to the same card (or
plain link) you'd get without the sidecar.

| Provider | Handles | Settings |
|----------|---------|----------|
| `youtube` | videos, Shorts, live, playlists on `youtube.com` / `youtu.be` | `STUGAN_PREVIEWS_YOUTUBE_API_KEY` (optional) |
| `instagram` | public posts, reels and IGTV on `instagram.com` | none |
| `reddit` | post links on `reddit.com` (any subdomain) and `redd.it` | `STUGAN_PREVIEWS_REDDIT_CLIENT_ID`, `_CLIENT_SECRET`, `_USER_AGENT` (all optional) |
| `opengraph` | everything else (`og:*`, `<title>`, charset-aware) | none |

**YouTube.** With an API key ([YouTube Data API v3](https://developers.google.com/youtube/v3/getting-started),
free quota) the card shows title, description, thumbnail, channel, duration,
views, likes and date. Without a key it uses YouTube's keyless oEmbed endpoint:
title, channel and thumbnail only.

**Instagram.** Fetches the post page the way a link unfurler (Facebook,
Discord) does, with no login, token or official API, and unpacks the author,
like and comment counts, date, caption and image from its Open Graph tags.
That text is not a stable API: Instagram can change it, rate-limit or block
server IPs at any time. When that happens the provider fails quietly and the
link shows as a plain card (run with `STUGAN_PREVIEWS_LOG_LEVEL=debug` to see
why). Private posts never work.

**Reddit.** Anonymous `.json` access is blocked by Reddit, so without
credentials the card comes from Reddit's keyless oEmbed endpoint: title and
subreddit only. For score, upvote ratio, comment count, author, date, a text
snippet and the thumbnail, register your **own** Reddit app at
<https://www.reddit.com/prefs/apps> (type *script*, or *installed app* which
has no secret) and set its client id (and secret, for a script app) in the
sidecar. It uses an app-only token, cached until it expires. Use credentials
you registered yourself, not another client's; borrowed keys violate Reddit's
terms and get revoked. Reddit's API access rules change, so check they still
allow your use. NSFW and spoiler posts get no text or image. If the credentials
are wrong or rate-limited, the card falls back to the keyless one. Set
`STUGAN_PREVIEWS_REDDIT_USER_AGENT` to something like
`linux:stugan-previews:1.0 (by /u/yourname)`, as Reddit asks.

## Adding a provider

Each site is one file in `internal/previews/` implementing:

```go
type Provider interface {
    Name() string
    Match(u *url.URL) bool   // cheap, no I/O
    Fetch(ctx context.Context, f *Fetcher, u *url.URL) (*Card, error)
}
```

1. Create `internal/previews/<site>.go`. Fetch only through the `*Fetcher` it's
   handed (it carries the SSRF guard), and read any key with
   `Setting(env, "<site>", "api_key")`, which maps to
   `STUGAN_PREVIEWS_<SITE>_API_KEY`.
2. Add it to `Providers()` in `provider.go`, before `openGraph{}`.
3. Fill `Card.Site` and `Card.Details` for the extras the client renders.
4. Document its settings in the table above.

A provider whose key is missing should either degrade (as YouTube does) or
return an error from `Fetch` so the chain falls through.

## Security notes

- The sidecar authenticates every `/v1/*` request with the shared secret
  (constant-time compare). `/healthz` is open. Don't publish the port.
- All fetching goes through `internal/safehttp`: public addresses only,
  checked at dial time on every redirect hop, 5-hop cap, 8 s timeout.
- Responses are size-capped (512 KB of HTML, 1 MB of API JSON, 10 MB of media),
  and the media proxy sends `nosniff` and a sandboxing CSP so a hostile SVG
  can't run script on your origin.
- API keys live only in the sidecar's environment, never in stugan.
