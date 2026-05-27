import { reactive, watch } from "vue";

export const THEMES = ["dark", "midnight", "light"] as const;
export type Theme = (typeof THEMES)[number];

interface Settings {
  theme: Theme;
  muted: string[]; // buffer keys ("network buffer")
}

const KEY = "stugan.settings";

function load(): Settings {
  try {
    const s = JSON.parse(localStorage.getItem(KEY) || "{}");
    return {
      theme: THEMES.includes(s.theme) ? s.theme : "dark",
      muted: Array.isArray(s.muted) ? s.muted : [],
    };
  } catch {
    return { theme: "dark", muted: [] };
  }
}

export const settings = reactive<Settings>(load());

watch(
  settings,
  (s) => {
    localStorage.setItem(KEY, JSON.stringify(s));
    document.documentElement.dataset.theme = s.theme;
  },
  { deep: true, immediate: true },
);

export function isMuted(key: string): boolean {
  return settings.muted.includes(key);
}

export function toggleMute(key: string) {
  const i = settings.muted.indexOf(key);
  if (i >= 0) settings.muted.splice(i, 1);
  else settings.muted.push(key);
}
