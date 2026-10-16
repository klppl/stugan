<script setup lang="ts">
import { onMounted, onUnmounted } from "vue";
import { isMac, NUM_MOD } from "../ui";

// KeyboardShortcuts is the cheat sheet opened with Ctrl/Cmd+/ (or from the
// Cmd-K palette). It only lists bindings; App.vue and ChatInput own them.
const emit = defineEmits<{ close: [] }>();

const MOD = isMac ? "⌘" : "Ctrl";
const ALT = isMac ? "⌥" : "Alt";
const NUM = isMac ? "⌃" : NUM_MOD;

const groups: { title: string; rows: [string[], string][] }[] = [
  {
    title: "Moving around",
    rows: [
      [[MOD, "K"], "Jump to a channel or run a command"],
      [[ALT, "↑ / ↓"], "Previous / next buffer"],
      [[ALT, "Shift", "↓"], "Next buffer with unread messages (mentions first)"],
      [[ALT, "Shift", "↑"], "Previous buffer with unread messages"],
      ...(isMac ? [] : ([[[ALT, "A"], "Next unread buffer (irssi)"]] as [string[], string][])),
      [[NUM, "1 – 9"], "Go to buffer 1–9 in sidebar order (0 is the 10th)"],
      [[MOD, "/"], "Show this list"],
    ],
  },
  {
    title: "Writing",
    rows: [
      [["Enter"], "Send"],
      [["Shift", "Enter"], "New line"],
      [["Tab"], "Complete a nick, #channel, /command or :emoji — press again for the next match"],
      [["Shift", "Tab"], "Previous match"],
      [["Esc"], "Undo the completion"],
      [["↑ / ↓"], "Recall lines you sent in this buffer"],
    ],
  },
  {
    title: "In the chat",
    rows: [
      [["Click nick"], "Nick menu (whois, DM, ignore, op…)"],
      [["Shift", "Click nick"], "Address them in the input"],
      [["Shift", "F10"], "Buffer menu for the focused sidebar row"],
    ],
  },
];

function onKey(e: KeyboardEvent) {
  if (e.key === "Escape") {
    e.preventDefault();
    emit("close");
  }
}
onMounted(() => window.addEventListener("keydown", onKey));
onUnmounted(() => window.removeEventListener("keydown", onKey));
</script>

<template>
  <div class="palette-overlay" @click.self="emit('close')">
    <div class="shortcuts" role="dialog" aria-modal="true" aria-labelledby="shortcuts-title">
      <header class="shortcuts-head">
        <h2 id="shortcuts-title">Keyboard shortcuts</h2>
        <button type="button" class="ghost" aria-label="Close" @click="emit('close')">Esc</button>
      </header>
      <section v-for="g in groups" :key="g.title" class="shortcuts-group">
        <h3>{{ g.title }}</h3>
        <dl>
          <template v-for="([keys, what], i) in g.rows" :key="i">
            <dt>
              <template v-for="(k, j) in keys" :key="j"><kbd>{{ k }}</kbd><span v-if="j < keys.length - 1" class="plus">+</span></template>
            </dt>
            <dd>{{ what }}</dd>
          </template>
        </dl>
      </section>
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
  padding: 10vh 12px 12px;
  z-index: 60;
}
.shortcuts {
  background: var(--bg);
  color: var(--fg);
  border: 1px solid var(--border);
  border-radius: 8px;
  width: min(600px, 100%);
  max-height: 80vh;
  overflow-y: auto;
  padding: 6px 18px 16px;
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.4);
}
.shortcuts-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  position: sticky;
  top: 0;
  background: var(--bg);
  padding: 10px 0 6px;
}
.shortcuts-head h2 {
  font-size: 1.05em;
  margin: 0;
}
.shortcuts-group h3 {
  font-size: 0.75em;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--fg-dim);
  margin: 14px 0 6px;
}
.shortcuts-group dl {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 6px 16px;
  margin: 0;
  align-items: baseline;
}
.shortcuts-group dt {
  white-space: nowrap;
}
.shortcuts-group dd {
  margin: 0;
  color: var(--fg-dim);
  font-size: 0.92em;
}
kbd {
  font: inherit;
  font-size: 0.8em;
  border: 1px solid var(--border);
  border-bottom-width: 2px;
  border-radius: 4px;
  padding: 0 6px;
  background: var(--bg-alt);
}
.plus {
  color: var(--fg-dim);
  margin: 0 3px;
  font-size: 0.8em;
}
@media (max-width: 520px) {
  .shortcuts-group dl {
    grid-template-columns: 1fr;
    gap: 2px;
  }
  .shortcuts-group dd {
    margin-bottom: 8px;
  }
}
</style>
