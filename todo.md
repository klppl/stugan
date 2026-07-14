# TODO — mobile UX feedback (2026-07-14)

Source: first outside tester feedback (Swedish), comparing stugan to TheLounge on a
phone. Overall verdict positive ("stugan feels snappier than TheLounge"), plus six
concrete things TheLounge does better.

**Status: all six shipped 2026-07-14** (client-only; verified 11/11 in Playwright
mobile emulation + desktop regression check against a scratch daemon on Libera).

- [x] **1. Send button collapsed the mobile keyboard** — `@mousedown.prevent` on
  the Send button plus a refocus in `submit()` (`ChatInput.vue`). Focus stays in
  the textarea after a button-send.
- [x] **2. Own messages visually distinct** — own message *body* now tinted with
  the existing per-theme `--self` var, not just the nick (`style.css`
  `.message.self .body`). Theme-configurable like the tester assumed.
- [x] **3. Mentions button toggles** — second press returns to the chat view
  (`connection.ts showMentions()`).
- [x] **4. Search focuses on open** — the mobile magnifier now focuses the
  revealed field on the next tick (`TopBar.vue toggleSearch()`).
- [x] **5. Text size** — base bumped 14 → 15px, driven by a `--font-size` root
  var, plus a "Text size" setting (13/14/15/16/18 px) in Settings
  (`settings.ts`, `Settings.vue`). Removed the mobile media query's hard-coded
  14px body override that would have masked the setting on phones.
- [x] **6. Topic on mobile** — topic stays hidden in the cramped bar, but tapping
  the channel name (marked with a ▾ caret, channels only) reveals it on its own
  wrapped row (`TopBar.vue`, `style.css` `.topic.mobile-open`).

## Possible follow-ups (not committed to)

- Self-body tint could get a Settings toggle if `var(--self)` on the whole body
  feels loud in some themes; today it's CSS-only.
- Mentions toggle always returns to `chat`; if the user came from the search
  view it doesn't restore *search*. Remember the previous view if anyone notices.
- Topic reveal shows the raw topic; a fuller channel-details sheet (modes,
  member count, topic setter/time) could hang off the same tap target later.
- Font-size setting is per-browser (localStorage), like the theme. Fine for now;
  server-side settings sync would be its own project.
