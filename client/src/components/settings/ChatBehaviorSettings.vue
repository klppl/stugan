<script setup lang="ts">
import { settings } from "../../settings";
</script>

<template>
  <div class="settings-section">
    <div class="section-header">
      <h3 class="section-title">💬 Chat & Formatting</h3>
      <p class="section-desc">Configure message formatting, events folding, link previews, and typing state.</p>
    </div>

    <div class="settings-card">
      <label class="setting-row checkbox-row">
        <div class="setting-label">
          <span>Fold Join / Part Events</span>
          <span class="setting-hint">Group consecutive join, part, and quit messages to declutter chat log</span>
        </div>
        <input v-model="settings.foldEvents" type="checkbox" class="toggle-switch" />
      </label>

      <div class="setting-divider" />

      <label class="setting-row checkbox-row">
        <div class="setting-label">
          <span>Expand Link Previews</span>
          <span class="setting-hint">Automatically fetch and display rich card previews for HTTP links</span>
        </div>
        <input v-model="settings.expandLinkPreviews" type="checkbox" class="toggle-switch" />
      </label>

      <div class="setting-divider" />

      <label class="setting-row checkbox-row">
        <div class="setting-label">
          <span>Colored Nicks</span>
          <span class="setting-hint">Assign consistent colors to user nicks in chat and member list</span>
        </div>
        <input v-model="settings.coloredNicks" type="checkbox" class="toggle-switch" />
      </label>

      <div class="setting-divider" />

      <label class="setting-row checkbox-row">
        <div class="setting-label">
          <span>Reactions</span>
          <span class="setting-hint">Allow adding and viewing emoji reactions on messages (IRCv3 msgid required)</span>
        </div>
        <input v-model="settings.reactions" type="checkbox" class="toggle-switch" />
      </label>

      <div class="setting-divider" />

      <label class="setting-row checkbox-row">
        <div class="setting-label">
          <span>Send Typing Notifications</span>
          <span class="setting-hint">Broadcast typing indicators to channels when you type</span>
        </div>
        <input v-model="settings.sendTyping" type="checkbox" class="toggle-switch" />
      </label>

      <div class="setting-divider" />

      <label class="setting-row checkbox-row">
        <div class="setting-label">
          <span>Show Others' Typing</span>
          <span class="setting-hint">Display typing indicators when other users in the buffer are typing</span>
        </div>
        <input v-model="settings.showTyping" type="checkbox" class="toggle-switch" />
      </label>
    </div>
  </div>
</template>
