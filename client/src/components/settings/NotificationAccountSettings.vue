<script setup lang="ts">
import { ref } from "vue";
import { connection } from "../../connection";
import { enablePush } from "../../pwa";
import { authState, logout } from "../../auth";

const pushMsg = ref("");
const notifSupported = typeof Notification !== "undefined";

async function enableNotifications() {
  pushMsg.value = "requesting permission…";
  const perm = await Notification.requestPermission();
  if (perm !== "granted") {
    pushMsg.value = "Not enabled (permission denied)";
    return;
  }
  if (connection.hasCap("push")) {
    const ok = await enablePush();
    pushMsg.value = ok ? "Desktop & Push notifications enabled ✓" : "Desktop notifications enabled (Push service worker failed)";
  } else {
    pushMsg.value = "Desktop notifications enabled ✓";
  }
}
</script>

<template>
  <div class="settings-section">
    <div class="section-header">
      <h3 class="section-title">👤 Account & Notifications</h3>
      <p class="section-desc">Manage system notifications, session status, and user profile authentication.</p>
    </div>

    <div class="settings-card">
      <h4 class="card-subtitle">Notifications</h4>
      <div v-if="notifSupported" class="setting-row">
        <div class="setting-label">
          <span>Desktop & Push Notifications</span>
          <span class="setting-hint">Receive browser or WebPush notifications when mentioned or highlighted</span>
        </div>
        <button class="btn btn-primary btn-sm" @click="enableNotifications">Enable Notifications</button>
      </div>
      <p v-else class="hint">Browser desktop notifications are not supported on this platform.</p>
      <p v-if="pushMsg" class="push-status-msg">{{ pushMsg }}</p>
      <p class="hint">Tip: Individual channels can be muted by right-clicking them in the sidebar.</p>

      <div class="setting-divider" />

      <h4 class="card-subtitle">User Session</h4>
      <div v-if="authState.authEnabled" class="setting-row">
        <div class="setting-label">
          <span>Signed in as <strong>{{ authState.user }}</strong></span>
          <span class="setting-hint">Authenticated daemon session</span>
        </div>
        <button class="btn btn-danger-ghost btn-sm" @click="logout">Log Out</button>
      </div>
      <div v-else class="setting-row">
        <div class="setting-label">
          <span>Single-User Mode (Unauthenticated)</span>
          <span class="setting-hint">Running in local single-tenant daemon mode</span>
        </div>
      </div>
    </div>
  </div>
</template>
