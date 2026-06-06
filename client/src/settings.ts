import { reactive, watch } from "vue";

// Built-in themes are backed by CSS rules in style.css and can't be removed.
export const BUILTIN_THEMES = ["dark", "midnight", "light"] as const;

// The CSS custom properties a theme controls. A custom theme may set any
// subset; unspecified ones inherit the default (dark) values.
export const THEME_VARS = [
  "--bg",
  "--bg-alt",
  "--bg-sidebar",
  "--fg",
  "--fg-dim",
  "--accent",
  "--self",
  "--hl",
  "--border",
] as const;

// TEMPLATE is a ready-to-edit starting point shown in the install box.
export const TEMPLATE = `--bg: #1e2228;
--bg-alt: #262b33;
--bg-sidebar: #181b20;
--fg: #d4d7dd;
--fg-dim: #8b929e;
--accent: #5c9ded;
--self: #7ec07e;
--hl: #d97070;
--border: #000000;`;

export interface CustomTheme {
  name: string;
  vars: Record<string, string>;
}

interface Settings {
  theme: string;
  customThemes: CustomTheme[];
  foldEvents: boolean; // collapse runs of join/part/quit/nick lines
  coloredNicks: boolean; // colorize nicks by a hash of the name
  reactions: boolean; // show emoji reactions (off by default; most servers don't support it)
  sendTyping: boolean; // broadcast our own +typing notifications (opt-in: others can see when you type)
  showTyping: boolean; // display other people's typing notifications
}

const KEY = "stugan.settings";

function load(): Settings {
  try {
    const s = JSON.parse(localStorage.getItem(KEY) || "{}");
    return {
      theme: typeof s.theme === "string" ? s.theme : "dark",
      customThemes: Array.isArray(s.customThemes) ? s.customThemes : [],
      foldEvents: typeof s.foldEvents === "boolean" ? s.foldEvents : true,
      coloredNicks: typeof s.coloredNicks === "boolean" ? s.coloredNicks : true,
      reactions: typeof s.reactions === "boolean" ? s.reactions : false,
      sendTyping: typeof s.sendTyping === "boolean" ? s.sendTyping : false,
      showTyping: typeof s.showTyping === "boolean" ? s.showTyping : true,
    };
  } catch {
    return {
      theme: "dark",
      customThemes: [],
      foldEvents: true,
      coloredNicks: true,
      reactions: false,
      sendTyping: false,
      showTyping: true,
    };
  }
}

export const settings = reactive<Settings>(load());

// applyTheme reflects the selected theme onto the document. Built-in themes
// match a CSS rule via data-theme; a custom theme falls back to the default
// rule and layers its variables as inline overrides on :root.
function applyTheme() {
  const root = document.documentElement;
  for (const v of THEME_VARS) root.style.removeProperty(v);
  root.dataset.theme = settings.theme;
  const custom = settings.customThemes.find((t) => t.name === settings.theme);
  if (custom) {
    for (const [k, val] of Object.entries(custom.vars)) root.style.setProperty(k, val);
  }
}

watch(
  settings,
  (s) => {
    localStorage.setItem(KEY, JSON.stringify(s));
    applyTheme();
  },
  { deep: true, immediate: true },
);

// themeNames lists every selectable theme (built-in + installed).
export function themeNames(): string[] {
  return [...BUILTIN_THEMES, ...settings.customThemes.map((t) => t.name)];
}

export function isBuiltin(name: string): boolean {
  return (BUILTIN_THEMES as readonly string[]).includes(name);
}

// parseTheme extracts `--var: value` declarations from pasted CSS, ignoring
// anything that isn't a custom property and rejecting values that could
// smuggle in markup or scripts.
function parseTheme(css: string): Record<string, string> {
  const vars: Record<string, string> = {};
  const re = /(--[\w-]+)\s*:\s*([^;\n}]+)/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(css)) !== null) {
    const value = m[2].trim();
    if (value && !/[<>}]|javascript:/i.test(value)) vars[m[1]] = value;
  }
  return vars;
}

// installTheme validates and saves a custom theme. Returns an error string,
// or null on success.
export function installTheme(name: string, css: string): string | null {
  name = name.trim();
  if (!name) return "Give the theme a name.";
  if (isBuiltin(name)) return `"${name}" is a built-in theme name.`;
  const vars = parseTheme(css);
  if (Object.keys(vars).length === 0) return "No --variables found. Paste CSS custom properties.";
  const existing = settings.customThemes.findIndex((t) => t.name === name);
  if (existing >= 0) settings.customThemes[existing] = { name, vars };
  else settings.customThemes.push({ name, vars });
  settings.theme = name; // apply it immediately
  return null;
}

export function uninstallTheme(name: string) {
  settings.customThemes = settings.customThemes.filter((t) => t.name !== name);
  if (settings.theme === name) settings.theme = "dark";
}

// --- Easter egg -----------------------------------------------------------
// "mirc" is a hidden built-in theme: it has a CSS rule in style.css but is
// deliberately left out of BUILTIN_THEMES/themeNames(), so it never shows in
// the theme dropdown. The only way to reach it is the secret handshake in the
// sidebar (tap the "stugan" brand a few times — see Sidebar.vue), which calls
// toggleMircTheme(). Toggling off restores whatever theme was active before.
export const MIRC_THEME = "mirc";
let preMircTheme = "dark";

export function toggleMircTheme(): boolean {
  if (settings.theme === MIRC_THEME) {
    settings.theme = preMircTheme;
    return false;
  }
  preMircTheme = settings.theme;
  settings.theme = MIRC_THEME;
  return true;
}
