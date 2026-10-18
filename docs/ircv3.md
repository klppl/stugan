# IRCv3 support & roadmap

stugan leans on IRCv3 for its "modern IRC" feel. This is the canonical list
of what's negotiated/implemented today and what's still on the table, roughly
in priority order. Caps are requested in `internal/irc/conn.go`
(`SupportedCaps`) and the negotiated set is reported per-network via
`Conn.Caps()` → `NetworkDTO.caps`, so the client can gate cap-dependent UI.

## Implemented

Capabilities negotiated:

- `server-time` — every line carries the real timestamp (backlog, replay).
- `message-tags` — generic tag transport; powers msgids, typing, reactions.
- `echo-message` — our own sent lines come back with the server's msgid; the
  engine skips the local echo so there's one authoritative copy.
- `account-notify`, `extended-join`, `account-tag` — sender account is known
  on JOIN *and* on every message (member list shows accounts).
- `away-notify` — live away/back state in the member list.
- `multi-prefix`, `userhost-in-names` — full prefix sets and hostmasks in NAMES.
- `chghost`, `setname` — shown as folded "changed host" / "changed name" lines
  in every channel shared with the member (message kinds `chghost`/`setname`,
  grouped with joins, parts and nick changes).
- `invite-notify` — negotiated; an op sees other people's invites.
- `labeled-response` — negotiated; composes with the above.
- `standard-replies` — `FAIL`/`WARN`/`NOTE` rendered as system lines.
- `draft/chathistory` / `chathistory` — upstream server history: the
  `/chathistory` command (every subcommand) and automatic catch-up, below.
- `soju.im/bouncer-networks` (+ `-notify`) — manage soju's networks from the UI.
- SASL `SCRAM-SHA-256` alongside PLAIN and EXTERNAL (`sasl_scram`).
- `draft/message-redaction` — delete messages (`REDACT`); see below.
- `draft/read-marker` / `read-marker` — synchronized read positions via `MARKREAD`.

Features built on top:

- **Read Marker Sync** (`draft/read-marker` / `MARKREAD`) — read positions are
  synchronized across devices and clients. When you focus or read a channel
  in the web UI, SSH TUI, or a connected IRC bouncer client, `MARKREAD`
  advances the read marker in SQLite and notifies all attached bouncer clients
  and browser sessions.
- **Reactions** (`+draft/react` + `+draft/reply` over TAGMSG) — emoji
  reactions on a message by msgid. Inbound toggles a chip; a hover palette
  and chip clicks send them. Ephemeral (session-lived), not persisted.
- **Message redaction** (`draft/message-redaction`) — a hover ✕ on your own
  messages sends `REDACT`; inbound `REDACT` removes the message from the view
  and deletes it from SQLite history (by msgid), so it stays gone after a
  reload and from search.
- **Typing** (`+typing` TAGMSG) — typing indicators.
- **Friends / MONITOR** (`MONITOR`, numerics 730/731) — a per-network friends
  list (`NetworkParams.Monitor`, persisted) re-armed with `MONITOR +` on each
  register. 730/731 become `EvMonitor`, updating `Network.MonitorOnline`, which
  rides the network snapshot to a sidebar "friends" section (presence dots,
  click-to-DM); a toast fires when a friend comes online. Add/remove from the
  nick context menu.
- **Multiline** (`draft/multiline` over `BATCH`) — a message with embedded
  newlines is sent as one logical block: `conn.Message` wraps the lines in a
  `BATCH +ref draft/multiline` (falling back to one PRIVMSG per line when the
  cap is absent). Inbound, a `BATCH` state machine in `internal/irc` buffers the
  members and reassembles them (honoring `draft/multiline-concat`) into one
  message via the normal translate path. The composer is a textarea (Enter
  sends, Shift+Enter inserts a newline); bodies render with `pre-wrap`.

### Upstream chathistory

- **`/chathistory`** — `[latest] [n]`, `before|after|around <ref> [n]`,
  `between <ref> <ref> [n]`, `targets [<ref> <ref>] [n]`. A `ref` is `msgid=ID`,
  `timestamp=TIME`, a bare date (`2024-05-01`) or RFC 3339 time, or `*`. Limits
  are clamped to 1000. Replayed lines arrive as normal messages (the unique
  msgid index drops any we already hold) in the order requested; stugan's own
  history is ordered by arrival, so `before`/`between`/`around` results appear
  after the live tail rather than slotting into place.
- **Catch-up** (`internal/core/chathistory.go`) — on registering, stugan sends
  `CHATHISTORY TARGETS` from the newest conversational line it holds (local
  system lines are ignored: they are stamped after the gap). Each query with
  news gets a buffer and a `CHATHISTORY AFTER` from its own newest line; each
  channel gets one when we join it. A full page (100) chains the next from the
  newest line received, up to 10 pages per buffer, and stops if a page makes no
  progress. Everything newer than what is stored appends in order. It needs a
  stored baseline, so a brand-new network fetches nothing automatically (use
  `/chathistory`). Soju and Ergo send no automatic replay to clients that
  negotiated chathistory, so this is what fills the offline gap.
- Batches (`internal/irc/conn.go`): messages inside a `chathistory` batch are
  counted and the batch's end reported to the engine; any non-message line in
  one (JOIN/PART/…) is dropped so a replay can never rewrite live membership.
- Replays don't ring: web push and desktop notifications skip lines older than
  15 minutes (they still count as unread and appear in the missed-highlights
  digest).

### Bouncer networks (soju)

When the connection is a soju *control* connection (SASL login of just `user`,
no `/network`), soju offers `soju.im/bouncer-networks`. stugan sends
`BOUNCER LISTNETWORKS` on connect and follows `-notify` pushes; the list rides
the network snapshot (`NetworkDTO.bouncer_networks`). The network's settings
dialog shows a "Bouncer networks" section to **add** (`ADDNETWORK`), **remove**
(`DELNETWORK`) and **open** one — which creates a stugan network copying the
control network's server and credentials with the login `user/<network>`
(keeping any `@client` suffix). Renaming (`CHANGENETWORK`) isn't wired up.

## Roadmap

Nothing in the must-have list is outstanding. Candidates:

- Slot `before`/`between` history into place (needs the store to page by time,
  not insertion order).
- `soju.im/bouncer-networks` `CHANGENETWORK`.
- `utf8only`, `draft/channel-rename`, `draft/metadata`,
  `draft/account-registration`, WHOX-driven richer member data, `draft/pre-away`.
