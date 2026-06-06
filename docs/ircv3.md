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
- `chghost`, `setname`, `invite-notify` — negotiated (not yet surfaced in UI).
- `labeled-response` — negotiated; composes with the above.
- `standard-replies` — `FAIL`/`WARN`/`NOTE` rendered as system lines.
- `draft/chathistory` — best-effort server-side history (`/chathistory`).
- `draft/message-redaction` — delete messages (`REDACT`); see below.

Features built on top:

- **Reactions** (`+draft/react` + `+draft/reply` over TAGMSG) — emoji
  reactions on a message by msgid. Inbound toggles a chip; a hover palette
  and chip clicks send them. Ephemeral (session-lived), not persisted.
- **Message redaction** (`draft/message-redaction`) — a hover ✕ on your own
  messages sends `REDACT`; inbound `REDACT` removes the message from the view.
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

## Roadmap (must-haves, prioritized)

1. **`draft/read-marker`** (`MARKREAD`). Sync the read position across
   devices/clients server-side, instead of the current client-only unread
   divider. Pairs naturally with the existing divider work.

2. **Redaction persistence.** Today a `REDACT` only removes the live copy;
   the message still sits in the SQLite history and returns on reload. Delete
   (or tombstone) by msgid in `internal/store` so redactions survive.

3. **SASL SCRAM** (`SCRAM-SHA-256`). We do PLAIN and EXTERNAL/CertFP; SCRAM
   avoids sending the password even once. Depends on girc support.

4. **`draft/chathistory` depth** — `CHATHISTORY TARGETS`, proper pagination
   (BEFORE/AFTER/BETWEEN), and unread sync, to lean on server history where
   available instead of only the local SQLite backlog.

5. **Surface `chghost` / `setname`** — we negotiate them but don't reflect
   host/realname changes anywhere user-visible.

6. **`soju.im/bouncer-networks`** — manage bouncer-side networks from the UI
   when connected through soju.

Lower priority / nice-to-have: `utf8only`, `draft/channel-rename`,
`draft/metadata`, `draft/account-registration`, WHOX-driven richer member
data, `draft/pre-away`.
