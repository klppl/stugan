<script setup lang="ts">
import { ref } from "vue";
import {
  settings,
  themeNames,
  installTheme,
  uninstallTheme,
  TEMPLATE,
  PRESET_THEMES,
  FONT_SIZES,
} from "../../settings";
import type { PresetTheme } from "../../settings";

// Theme installer state
const showInstall = ref(false);
const themeName = ref("");
const themeCss = ref(TEMPLATE);
const themeError = ref("");

function doInstall() {
  themeError.value = installTheme(themeName.value, themeCss.value) ?? "";
  if (!themeError.value) {
    showInstall.value = false;
    themeName.value = "";
    themeCss.value = TEMPLATE;
  }
}

function usePreset(p: PresetTheme) {
  themeName.value = p.name;
  themeCss.value = p.css;
  themeError.value = "";
}
</script>

<template>
  <div class="settings-section">
    <div class="section-header">
      <h3 class="section-title">🎨 Appearance</h3>
      <p class="section-desc">Customize the look, color themes, and font size of stugan.</p>
    </div>

    <div class="settings-card">
      <label class="setting-row">
        <div class="setting-label">
          <span>Theme</span>
          <span class="setting-hint">Choose your preferred visual theme</span>
        </div>
        <select v-model="settings.theme" class="setting-input">
          <option v-for="t in themeNames()" :key="t" :value="t">{{ t }}</option>
        </select>
      </label>

      <!-- Custom themes installed -->
      <div v-for="t in settings.customThemes" :key="t.name" class="setting-row custom-theme-item">
        <div class="setting-label">
          <span>Custom theme: <strong>{{ t.name }}</strong></span>
        </div>
        <button class="btn btn-sm btn-danger-ghost" @click="uninstallTheme(t.name)">Remove</button>
      </div>

      <div class="setting-row">
        <div class="setting-label">
          <span>Install Custom Theme</span>
          <span class="setting-hint">Create or paste CSS theme variables</span>
        </div>
        <button class="btn btn-sm" @click="showInstall = !showInstall">
          {{ showInstall ? "Cancel" : "Install Theme…" }}
        </button>
      </div>

      <div v-if="showInstall" class="install-theme-panel">
        <p class="hint">Start from a preset theme, or paste custom CSS variables below.</p>
        <div class="presets-grid">
          <button
            v-for="p in PRESET_THEMES"
            :key="p.name"
            class="preset-card"
            :title="p.blurb"
            @click="usePreset(p)"
          >
            <span
              class="swatch"
              :style="{
                background: p.css.match(/--bg:\s*([^;]+)/)?.[1],
                borderColor: p.css.match(/--accent:\s*([^;]+)/)?.[1]
              }"
            />
            <span class="preset-name">{{ p.name }}</span>
          </button>
        </div>

        <input
          v-model="themeName"
          class="setting-text-input"
          placeholder="Theme name (e.g. Solarized Dark)"
        />
        <textarea
          v-model="themeCss"
          class="setting-textarea code-font"
          rows="8"
          spellcheck="false"
          placeholder="Paste CSS variables, e.g. --bg: #002b36;"
        />
        <p class="hint">
          Paste <code>--var: value;</code> lines. Unset variables inherit the dark theme baseline.
        </p>
        <p v-if="themeError" class="login-error">{{ themeError }}</p>
        <button class="btn btn-primary" @click="doInstall">Install Theme</button>
      </div>

      <div class="setting-divider" />

      <label class="setting-row">
        <div class="setting-label">
          <span>Text Size</span>
          <span class="setting-hint">Base font size for message log and buffers</span>
        </div>
        <select v-model.number="settings.fontSize" class="setting-input">
          <option v-for="s in FONT_SIZES" :key="s" :value="s">{{ s }} px</option>
        </select>
      </label>
    </div>
  </div>
</template>
