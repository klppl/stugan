# Theming

stugan's appearance is driven entirely by a small set of **CSS custom
properties** (variables) on the document root. A theme is just a set of values
for those variables. There are three built-in themes and an in-app installer
for your own — no rebuild, no file editing, no server restart.

## How it works

Every color in the UI is `var(--something)`. The built-in themes declare those
variables in `client/src/style.css`, scoped by a `data-theme` attribute on
`:root`:

```css
:root,
:root[data-theme="dark"]      { --bg: #1e2228; --fg: #d4d7dd; /* … */ }
:root[data-theme="midnight"]  { --bg: #0b0f1a; --fg: #c7d0e0; /* … */ }
:root[data-theme="light"]     { --bg: #ffffff; --fg: #1c1e21; /* … */ }
```

Selecting a theme (`client/src/settings.ts → applyTheme()`) sets
`document.documentElement.dataset.theme`, which activates the matching rule. A
**custom** theme falls back to the default (dark) rule and then layers its own
variables as inline `style` overrides on `:root` — so you only need to specify
the variables you want to change; the rest inherit the dark defaults.

Your selection and any installed themes are saved in `localStorage`
(`stugan.settings`), so they persist per browser. Themes are a **client-side,
per-browser** preference — they are not synced to the server or shared between
users.

## The theme variables

A theme controls these nine variables (`THEME_VARS` in `settings.ts`). A custom
theme may set any subset.

| Variable | Controls | Dark default |
|----------|----------|--------------|
| `--bg` | main background (chat area) | `#1e2228` |
| `--bg-alt` | raised surfaces: input, dialogs, hovered rows, members panel | `#262b33` |
| `--bg-sidebar` | the network/buffer sidebar background | `#181b20` |
| `--fg` | primary text | `#d4d7dd` |
| `--fg-dim` | secondary/muted text: timestamps, metadata, placeholders | `#8b929e` |
| `--accent` | links, the active buffer, buttons, focus highlights | `#5c9ded` |
| `--self` | your own nick / messages you sent | `#7ec07e` |
| `--hl` | highlights/mentions, unread-highlight badges, errors | `#d97070` |
| `--border` | dividers and outlines between panels | `#000000` |

> Note: nick colors (when "Colored nicks" is on) are generated from a hash of
> each nick and are **not** part of the theme — see
> [frontend.md](frontend.md) (`nickColor.ts`).

## Creating a theme

1. Open **Settings → Theme** (the ⚙ button in the top bar).
2. In the "Install theme" box, give it a **name** and paste a block of CSS
   custom-property declarations.
3. Click install. The theme is validated, saved, and applied immediately. It
   then appears in the theme dropdown alongside the built-ins.

Start from this template (it's the dark theme's values, pre-filled in the
install box — `TEMPLATE` in `settings.ts`):

```css
--bg: #1e2228;
--bg-alt: #262b33;
--bg-sidebar: #181b20;
--fg: #d4d7dd;
--fg-dim: #8b929e;
--accent: #5c9ded;
--self: #7ec07e;
--hl: #d97070;
--border: #000000;
```

You can omit any line you don't want to change — unspecified variables inherit
the dark defaults. A minimal "just make links pink" theme is valid:

```css
--accent: #ff6ac1;
```

To edit an installed theme, install again under the **same name** — it
overwrites. Use the remove control next to a custom theme to delete it (if it
was the active theme, the selection reverts to `dark`).

## What the installer accepts

The parser (`parseTheme` in `settings.ts`) is deliberately strict — it reads
**only** CSS custom properties and sanitizes their values:

- It matches `--name: value` declarations via the regex
  `/(--[\w-]+)\s*:\s*([^;\n}]+)/g`. Anything that isn't a `--variable`
  declaration (selectors, regular CSS properties, `@`-rules, comments) is
  **ignored**, so you can paste a whole `:root { … }` block and only the
  variables are picked up.
- A value is **rejected** if it contains `<`, `>`, `}`, or `javascript:` — this
  blocks attempts to smuggle markup or scripts through a value. Rejected values
  are simply dropped.
- Variable **names** are free-form (`--[\w-]+`), so you can even define your own
  beyond the nine above — but only the nine in `THEME_VARS` are referenced by
  the stylesheet, so extra variables have no visual effect today.
- Install fails (with a message) if the name is blank, collides with a built-in
  name (`dark`/`midnight`/`light`), or the pasted text yields zero variables.

Values can be any CSS color the browser understands — hex (`#rrggbb`,
`#rgb`), `rgb()` / `rgba()`, `hsl()`, or named colors.

## Worked example — a warm "solarized-ish" dark theme

```css
--bg: #002b36;
--bg-alt: #073642;
--bg-sidebar: #00212b;
--fg: #93a1a1;
--fg-dim: #586e75;
--accent: #268bd2;
--self: #859900;
--hl: #cb4b16;
--border: #073642;
```

Paste that into the install box under the name `solarized`, install, and it's
live.

## Sharing a theme

A custom theme is just the CSS block you pasted, so share it as a snippet — in
a gist, a message, or this repo's discussions. There's no theme file format and
no install-from-URL; the recipient pastes it into their own install box. (The
stored shape, if you want to inspect it, is `{ name, vars }` under
`customThemes` in the `stugan.settings` localStorage key.)

## Adding a built-in theme (for contributors)

To ship a new theme with stugan rather than install it at runtime:

1. Add a `:root[data-theme="yourname"] { … }` block in `client/src/style.css`
   setting the nine variables.
2. Add `"yourname"` to `BUILTIN_THEMES` in `client/src/settings.ts` so it shows
   in the dropdown and is protected from being overwritten by a same-named
   custom theme.
3. Rebuild the client (`npm run build`).

Built-in themes are backed by stylesheet rules (not inline overrides) and can't
be removed by users.
</content>
