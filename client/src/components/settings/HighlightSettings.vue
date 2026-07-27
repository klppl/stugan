<script setup lang="ts">
import { ref, watch } from "vue";
import { connection } from "../../connection";

const hlPatterns = ref(connection.store.highlight.patterns.join("\n"));
const hlExceptions = ref(connection.store.highlight.exceptions.join("\n"));
const hlSaved = ref(false);
let hlPending = false;

function saveHighlight() {
  const toList = (s: string) =>
    s
      .split("\n")
      .map((l) => l.trim())
      .filter(Boolean);
  hlPending = true;
  connection.setHighlight(toList(hlPatterns.value), toList(hlExceptions.value));
}

watch(
  () => connection.store.highlight,
  (h) => {
    hlPatterns.value = h.patterns.join("\n");
    hlExceptions.value = h.exceptions.join("\n");
    if (hlPending) {
      hlPending = false;
      hlSaved.value = true;
      setTimeout(() => (hlSaved.value = false), 2000);
    }
  },
);
</script>

<template>
  <div class="settings-section">
    <div class="section-header">
      <h3 class="section-title">🔔 Highlights</h3>
      <p class="section-desc">Define custom keywords and exception patterns for highlight notifications.</p>
    </div>

    <div class="settings-card">
      <p class="hint">
        Messages containing these regular expressions (in addition to your nick) trigger highlights and notifications. Case-insensitive, one pattern per line.
      </p>

      <div class="form-group">
        <label class="form-label">
          <span>Highlight Keywords</span>
          <span class="setting-hint">One regular expression per line</span>
        </label>
        <textarea
          v-model="hlPatterns"
          class="setting-textarea code-font"
          rows="5"
          spellcheck="false"
          placeholder="release&#10;\bdeploy\b&#10;urgent"
        />
      </div>

      <div class="form-group">
        <label class="form-label">
          <span>Exceptions</span>
          <span class="setting-hint">Patterns that prevent a highlight even if keywords match</span>
        </label>
        <textarea
          v-model="hlExceptions"
          class="setting-textarea code-font"
          rows="4"
          spellcheck="false"
          placeholder="bot-notice&#10;automated-release"
        />
      </div>

      <div class="row-action">
        <span v-if="hlSaved" class="saved-indicator">Saved ✓</span>
        <button class="btn btn-primary" @click="saveHighlight">Save Highlights</button>
      </div>
    </div>
  </div>
</template>
