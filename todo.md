# TODO — mobile UX feedback (2026-07-14)

Source: first outside tester feedback (Swedish), comparing stugan to TheLounge on a
phone. Overall verdict positive ("stugan feels snappier than TheLounge"), plus six
concrete things TheLounge does better. Each item below is grounded in the current
code; two of the six turned out to be already implemented but invisible/too subtle
on mobile, so they're framed as what's actually missing rather than as reported.

## 1. Send button collapses the mobile keyboard (bug)

Tapping Send moves focus off the textarea to the button, so the keyboard closes
and you can't keep typing. TheLounge keeps the keyboard up.

- The Send control is a plain submit `<button>` in `client/src/components/ChatInput.vue:369-383`
  with no `@mousedown.prevent`, and `submit()` (`ChatInput.vue:259-276`) never
  refocuses the input.
- The fix pattern already exists in the same file: the autocomplete list items use
  `@mousedown.prevent` (`ChatInput.vue:351`) exactly to avoid stealing focus, and a
  `focus()` method is exposed at `ChatInput.vue:312` but never called after send.
- Fix: `@mousedown.prevent` (+ touchstart equivalent) on the Send button, and/or
  refocus `inputEl` in `submit()`. Verify on a real phone or Playwright mobile
  emulation — desktop won't show the keyboard behavior.
- Effort: small.

## 2. Own messages should be visually distinct (enhancement)

Reported as "own text has a different color in TheLounge". Partially implemented:
`msg.self` exists end-to-end and the **nick** is colored via `--self`
(`MessageItem.vue:123,136,149`, `style.css:835-837`), but the message **body** looks
identical to everyone else's — on a phone with narrow nick columns that's easy to
miss. TheLounge tints the whole message text.

- Fix: style the `.message.self .body` (color or subtle tint) using the existing
  `--self` var; it's already defined per-theme (`style.css:9,20,31,47`) and in
  custom themes (`settings.ts:27,55,68,81,94,107`), so it stays theme-configurable
  like the tester assumed TheLounge's is.
- Consider making it a toggle in Settings if a full-body tint feels loud.
- Effort: small.

## 3. Mentions button should toggle (bug-ish)

Tapping Mentions again should return you to the chat; today it's one-way.

- `TopBar.vue:117-121` calls `connection.showMentions()`, which only sets
  `store.view = "mentions"` (`connection.ts:1064-1066`). The only way back is
  selecting a buffer (`connection.ts:1025`).
- Fix: if `store.view === 'mentions'`, return to the previously active buffer/view
  (needs remembering what was active before opening mentions). Same pattern would
  apply to any future full-pane views.
- Effort: small.

## 4. Search field should get focus when opened (bug-ish)

On mobile the magnifier button only flips a CSS class (`searchOpen` →
`.mobile-open`, `TopBar.vue:109-115`, `style.css:1898-1904`); the revealed input
(`TopBar.vue:100-108`) is never focused, so you have to tap it again before typing.

- Fix: add a `ref` on the search input and `nextTick(() => el.focus())` when
  `searchOpen` becomes true. Note the input already has `font-size: 16px` on mobile
  to prevent iOS zoom-on-focus (`style.css:1902`), so programmatic focus is safe.
- Effort: trivial.

## 5. Text size: slightly larger + user-configurable (enhancement)

Tester finds TheLounge's text size more comfortable ("a notch bigger"). Ours is
hard-coded `body { font-size: 14px }` (`style.css:376`) with no setting.

- Two parts:
  a. Bump the mobile/base message font size (TheLounge default is 16px-ish on
     mobile; the chat textarea and search already use 16px on mobile).
  b. Add a font-size setting to `settings.ts` (the `Settings` interface at
     `settings.ts:113-121` has no font option) applied as a CSS var / root
     font-size, exposed in the Settings UI. Element sizes already use relative
     `em`s in most places, so a root-size approach should cascade cleanly.
- Effort: (a) trivial, (b) small.

## 6. Topic not visible on mobile (gap, not a missing feature)

Topic support is fully implemented end-to-end (proto `proto.go:125`, engine
`engine.go:1532-1539`, client `connection.ts:54,203,816`, displayed + click-to-edit
in `TopBar.vue:78-96`) — but on phones it is deliberately hidden:
`style.css:1881-1884` sets `.topbar .topic { display: none }` for lack of space.
The tester ("can't find it in stugan") is on mobile, so to them it doesn't exist.

- Fix: give mobile users *some* way to see the topic, e.g. tap the channel name to
  expand/reveal the topic (it's already tappable-to-edit on desktop), or a channel
  details sheet from the top bar. Don't just un-hide it — the one-line bar
  genuinely has no room.
- Effort: small-medium depending on chosen affordance.

## Suggested order

Quick wins first — 4 (search focus), 1 (send focus), 3 (mentions toggle), 5a (base
size bump), 2 (self-body tint) are all small and mostly independent; then 5b
(font-size setting) and 6 (mobile topic affordance) as slightly larger pieces. All
are client-only; no Go/proto changes needed for any of them.
