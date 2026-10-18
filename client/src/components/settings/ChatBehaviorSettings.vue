<script setup lang="ts">
import { ref, watch } from "vue";
import { settings, isAwayMinutes, MAX_AWAY_MINUTES } from "../../settings";

// awayDraft holds what's typed in the minutes box; it's committed only when
// valid, and snaps back to the saved value on blur if it isn't.
const awayDraft = ref<number | string>(settings.awayMinutes);
watch(
  () => settings.awayMinutes,
  (v) => (awayDraft.value = v),
);
function commitAway() {
  const v = Number(awayDraft.value);
  if (isAwayMinutes(v)) settings.awayMinutes = v;
  else awayDraft.value = settings.awayMinutes;
}
</script>

<template>
  <div class="settings-section">
    <div class="section-header">
      <h3 class="section-title">Chat & Formatting</h3>
      <p class="section-desc">Configure message formatting, events folding, link previews, and typing state.</p>
    </div>

    <div class="settings-card">
      <label class="setting-row">
        <div class="setting-label">
          <span>Message Layout</span>
          <span class="setting-hint">Classic IRC puts time, nick and text on one line. Cozy puts the nick and time above the text and stacks consecutive messages from the same person. Auto uses Cozy on phones and Classic on desktop.</span>
        </div>
        <select v-model="settings.messageLayout" class="setting-input">
          <option value="auto">Auto</option>
          <option value="classic">Classic IRC</option>
          <option value="cozy">Cozy</option>
        </select>
      </label>

      <div class="setting-divider" />

      <label class="setting-row checkbox-row">
        <div class="setting-label">
          <span>Fold Join / Part Events</span>
          <span class="setting-hint">Group consecutive join, part, and quit messages and server notices to declutter chat log</span>
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

      <div class="setting-divider" />

      <label class="setting-row checkbox-row">
        <div class="setting-label">
          <span>Reply and Quote Buttons</span>
          <span class="setting-hint">Show Reply and Quote on a message's hover toolbar</span>
        </div>
        <input v-model="settings.replyQuote" type="checkbox" class="toggle-switch" />
      </label>

      <div class="setting-divider" />

      <label class="setting-row checkbox-row">
        <div class="setting-label">
          <span>What You Missed on Return</span>
          <span class="setting-hint">Open a summary of mentions and unread channels when you come back after {{ settings.awayMinutes }} {{ settings.awayMinutes === 1 ? "minute" : "minutes" }} or more away</span>
        </div>
        <input v-model="settings.digestOnReturn" type="checkbox" class="toggle-switch" />
      </label>

      <label class="setting-row" :class="{ disabled: !settings.digestOnReturn }">
        <div class="setting-label">
          <span>Away After</span>
          <span class="setting-hint">Minutes stugan has to be out of view before coming back counts as returning</span>
        </div>
        <span class="away-input">
          <input
            v-model="awayDraft"
            type="number"
            inputmode="numeric"
            min="1"
            :max="MAX_AWAY_MINUTES"
            step="1"
            class="setting-input"
            :disabled="!settings.digestOnReturn"
            @change="commitAway"
            @blur="commitAway"
          />
          <span class="setting-hint">min</span>
        </span>
      </label>
    </div>
  </div>
</template>

<style scoped>
.away-input {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}
.away-input .setting-input {
  width: 5.5em;
}
.setting-row.disabled {
  opacity: 0.5;
}
</style>
