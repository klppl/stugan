<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import { connection } from "../connection";

// CommandPalette is a Ctrl/Cmd-K quick switcher: fuzzy-jump to any buffer on
// any network, plus a few global actions. Pure client-side over store state.
// The parent (App.vue) owns the open flag and the global key binding; the
// palette emits actions it can't perform itself (opening Settings).
const props = defineProps<{ open: boolean }>();
const emit = defineEmits<{ (e: "close"): void; (e: "settings"): void }>();

const store = connection.store;
const query = ref("");
const selected = ref(0);
const input = ref<HTMLInputElement | null>(null);

interface Item {
  key: string;
  label: string; // primary text (buffer / action name)
  sub: string; // secondary text (network name, or a hint)
  unread: number;
  highlight: number;
  run: () => void;
}

// Static global actions, always offered (subject to the fuzzy filter).
const actions = computed<Item[]>(() => [
  { key: "act:settings", label: "Settings", sub: "Open settings", unread: 0, highlight: 0, run: () => { emit("settings"); close(); } },
  { key: "act:mentions", label: "Mentions", sub: "View all mentions", unread: 0, highlight: 0, run: () => { connection.showMentions(); close(); } },
  { key: "act:digest", label: "What you missed", sub: "Activity since you were away", unread: 0, highlight: 0, run: () => { connection.openDigest(); close(); } },
]);

// Every buffer across every network becomes a jump target.
const buffers = computed<Item[]>(() => {
  const out: Item[] = [];
  for (const n of store.networks) {
    for (const b of n.buffers) {
      out.push({
        key: n.id + "\x1f" + b.name,
        label: b.name,
        sub: n.name,
        unread: b.unread,
        highlight: b.highlight,
        run: () => { connection.select(n.id, b.name); close(); },
      });
    }
  }
  return out;
});

// fuzzyScore returns a rank for how well needle matches haystack as an ordered
// subsequence (higher is better), or -1 for no match. Contiguous and
// start-of-string matches score higher so the obvious target sorts first.
function fuzzyScore(haystack: string, needle: string): number {
  if (!needle) return 0;
  const h = haystack.toLowerCase();
  const n = needle.toLowerCase();
  let score = 0;
  let hi = 0;
  let prevMatch = -2;
  for (let ni = 0; ni < n.length; ni++) {
    const c = n[ni];
    const found = h.indexOf(c, hi);
    if (found === -1) return -1;
    score += 1;
    if (found === prevMatch + 1) score += 5; // contiguous run
    if (found === 0) score += 8; // matches at the very start
    prevMatch = found;
    hi = found + 1;
  }
  return score;
}

const results = computed<Item[]>(() => {
  const q = query.value.trim();
  const all = [...buffers.value, ...actions.value];
  if (!q) {
    // No query: show actions first, then buffers with activity, then the rest.
    return [
      ...actions.value,
      ...buffers.value
        .slice()
        .sort((a, b) => b.highlight - a.highlight || b.unread - a.unread),
    ].slice(0, 50);
  }
  return all
    .map((it) => ({ it, s: Math.max(fuzzyScore(it.label, q), fuzzyScore(it.sub + " " + it.label, q) - 2) }))
    .filter((r) => r.s >= 0)
    .sort((a, b) => b.s - a.s || b.it.highlight - a.it.highlight || b.it.unread - a.it.unread)
    .slice(0, 50)
    .map((r) => r.it);
});

// Keep the selection in range whenever the result set changes.
watch(results, () => { if (selected.value >= results.value.length) selected.value = 0; });

// Reset and focus each time the palette opens.
watch(
  () => props.open,
  (open) => {
    if (open) {
      query.value = "";
      selected.value = 0;
      nextTick(() => input.value?.focus());
    }
  },
);

function close() {
  emit("close");
}

function activate(i: number) {
  const it = results.value[i];
  if (it) it.run();
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === "ArrowDown") {
    e.preventDefault();
    selected.value = Math.min(selected.value + 1, results.value.length - 1);
  } else if (e.key === "ArrowUp") {
    e.preventDefault();
    selected.value = Math.max(selected.value - 1, 0);
  } else if (e.key === "Enter") {
    e.preventDefault();
    activate(selected.value);
  } else if (e.key === "Escape") {
    e.preventDefault();
    close();
  }
}
</script>

<template>
  <div v-if="open" class="palette-overlay" @click.self="close()">
    <div class="palette" role="dialog" aria-label="Command palette">
      <input
        ref="input"
        v-model="query"
        class="palette-input"
        type="text"
        placeholder="Jump to a channel or run a command…"
        spellcheck="false"
        autocomplete="off"
        @keydown="onKeydown"
      />
      <ul class="palette-results">
        <li v-if="!results.length" class="palette-empty">No matches</li>
        <li
          v-for="(it, i) in results"
          :key="it.key"
          class="palette-item"
          :class="{ sel: i === selected }"
          @mouseenter="selected = i"
          @click="activate(i)"
        >
          <span class="pi-label">{{ it.label }}</span>
          <span class="pi-sub">{{ it.sub }}</span>
          <span class="pi-counts">
            <span v-if="it.highlight" class="badge hl">{{ it.highlight }}</span>
            <span v-else-if="it.unread" class="badge">{{ it.unread }}</span>
          </span>
        </li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.palette-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.45);
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding: 12vh 12px 12px;
  z-index: 60;
}
.palette {
  background: var(--bg);
  color: var(--fg);
  border: 1px solid var(--border);
  border-radius: 8px;
  width: min(560px, 100%);
  max-height: 70vh;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.4);
}
.palette-input {
  border: none;
  border-bottom: 1px solid var(--border);
  background: none;
  color: inherit;
  font: inherit;
  font-size: 1rem;
  padding: 12px 14px;
  outline: none;
}
.palette-results {
  list-style: none;
  margin: 0;
  padding: 4px;
  overflow-y: auto;
}
.palette-empty {
  padding: 14px;
  color: var(--fg-dim);
  text-align: center;
}
.palette-item {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 7px 10px;
  border-radius: 5px;
  cursor: pointer;
}
.palette-item.sel {
  background: color-mix(in srgb, var(--accent) 18%, transparent);
}
.pi-label {
  font-weight: 600;
}
.pi-sub {
  color: var(--fg-dim);
  font-size: 0.85em;
  flex: 1;
}
.pi-counts {
  display: flex;
  gap: 4px;
}
.badge {
  background: var(--border);
  color: var(--fg);
  border-radius: 9px;
  padding: 0 7px;
  font-size: 0.78em;
  line-height: 1.5;
}
.badge.hl {
  background: var(--accent);
  color: #fff;
}
</style>
