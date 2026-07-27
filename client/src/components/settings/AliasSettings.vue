<script setup lang="ts">
import { ref, watch } from "vue";
import { connection } from "../../connection";

function formatAliases(m: Record<string, string>): string {
  return Object.keys(m)
    .sort()
    .map((k) => `${k} = ${m[k]}`)
    .join("\n");
}

const aliasText = ref(formatAliases(connection.store.aliases));
const aliasSaved = ref(false);
let aliasPending = false;

function saveAliases() {
  const map: Record<string, string> = {};
  for (const line of aliasText.value.split("\n")) {
    const eq = line.indexOf("=");
    if (eq < 0) continue;
    const name = line.slice(0, eq).trim();
    const expansion = line.slice(eq + 1).trim();
    if (name && expansion) map[name] = expansion;
  }
  aliasPending = true;
  connection.setAliases(map);
}

watch(
  () => connection.store.aliases,
  (m) => {
    aliasText.value = formatAliases(m);
    if (aliasPending) {
      aliasPending = false;
      aliasSaved.value = true;
      setTimeout(() => (aliasSaved.value = false), 2000);
    }
  },
);
</script>

<template>
  <div class="settings-section">
    <div class="section-header">
      <h3 class="section-title">⚡ Command Aliases</h3>
      <p class="section-desc">Configure slash-command shortcuts and argument expansion rules.</p>
    </div>

    <div class="settings-card">
      <p class="hint">
        Create slash-command shortcuts, one <code>name = /expansion</code> per line. Typing <code>/name</code> executes the expansion. Use <code>$1</code>..<code>$9</code> for arguments, <code>$*</code> for all arguments, and <code>$2-</code> for remaining arguments.
      </p>

      <div class="form-group">
        <label class="form-label">
          <span>Alias Rules</span>
          <span class="setting-hint">Format: <code>alias = /command arguments</code></span>
        </label>
        <textarea
          v-model="aliasText"
          class="setting-textarea code-font"
          rows="8"
          spellcheck="false"
          placeholder="j = /join $*&#10;wii = /whois $1&#10;slap = /me slaps $1 around with a large trout"
        />
      </div>

      <div class="row-action">
        <span v-if="aliasSaved" class="saved-indicator">Saved ✓</span>
        <button class="btn btn-primary" @click="saveAliases">Save Aliases</button>
      </div>
    </div>
  </div>
</template>
