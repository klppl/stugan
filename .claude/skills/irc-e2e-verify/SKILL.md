---
name: irc-e2e-verify
description: Drive the daemon end-to-end against a real IRC network (Libera) with a throwaway Node WebSocket client to verify protocol/connection behavior. Use to confirm a protocol or IRC change works against a live server, since there is no mock IRC server.
disable-model-invocation: true
---

# End-to-end IRC verification

There is no mock IRC server. The established way to verify protocol/IRC
behavior (per CLAUDE.md) is: run the daemon against **Libera**
(`irc.libera.chat:6697`, TLS) with a random nick into a low-traffic channel,
then drive it with a throwaway **Node WebSocket client** that speaks `proto`
frames over `ws://127.0.0.1:8080/ws`.

## Steps

### 1. Build and run the daemon on a disposable home

```sh
cd "$CLAUDE_PROJECT_DIR"
( cd client && npm run build )      # only if client behavior is under test
go build -o stugan ./cmd/stugan
./stugan -home ./dev &              # disposable config/data dir; serves :8080
```

Give it a moment to bind `:8080`. The `./dev` tree is already gitignored.

### 2. Seed a network (first run only)

With no `[[networks]]` configured, add one at runtime — either via the GUI or
by sending a `net:add` frame from the harness below. Use a **random nick**
(append digits) and a quiet channel (e.g. `#stugan-test<rand>` you create, or
a low-traffic existing one). Be a good Libera citizen: don't spam, disconnect
when done.

### 3. Drive it with the Node harness

Node 18+ has a global `WebSocket`. `scripts/e2e.mjs` (bundled with this skill)
connects, sends frames, and prints/asserts on what comes back. Run:

```sh
node "$CLAUDE_PROJECT_DIR/.claude/skills/irc-e2e-verify/scripts/e2e.mjs"
```

Edit the `STEPS` array in that script for the specific case under test:
send a frame (`{t, id, d}`), then assert on the next matching inbound frame.
Frame shapes are the source of truth in `internal/proto/proto.go` /
`client/src/proto/events.ts`.

### 4. Tear down

```sh
kill %1 2>/dev/null   # the backgrounded ./stugan
```

Report what was sent, what came back, and pass/fail per assertion. Quote the
actual frames on failure.
