<script setup lang="ts">
import { ref } from "vue";
import { settings, THEMES } from "../settings";
import { connection } from "../connection";
import { enablePush } from "../pwa";
import { authState, logout } from "../auth";

const emit = defineEmits<{ close: [] }>();
const pushMsg = ref("");

const notifSupported = typeof Notification !== "undefined";

async function enableNotifications() {
  pushMsg.value = "requesting…";
  const perm = await Notification.requestPermission();
  if (perm !== "granted") {
    pushMsg.value = "Not enabled (permission denied)";
    return;
  }
  // Desktop notifications now work while the tab is open. Also register Web
  // Push (notifications while away) when the server supports it.
  if (connection.hasCap("push")) {
    const ok = await enablePush();
    pushMsg.value = ok ? "Notifications + push enabled ✓" : "Desktop notifications enabled (push failed)";
  } else {
    pushMsg.value = "Desktop notifications enabled ✓";
  }
}
</script>

<template>
  <div class="settings-overlay" @click.self="emit('close')">
    <div class="settings">
      <h2>Settings</h2>

      <label class="row">
        <span>Theme</span>
        <select v-model="settings.theme">
          <option v-for="t in THEMES" :key="t" :value="t">{{ t }}</option>
        </select>
      </label>

      <div v-if="notifSupported" class="row">
        <span>Notifications</span>
        <button @click="enableNotifications">Enable</button>
      </div>
      <p v-if="pushMsg" class="hint">{{ pushMsg }}</p>
      <p class="hint">Mute a channel by right-clicking it in the sidebar.</p>

      <div v-if="authState.authEnabled" class="row">
        <span>Signed in as {{ authState.user }}</span>
        <button @click="logout">Log out</button>
      </div>

      <button class="close" @click="emit('close')">Close</button>
    </div>
  </div>
</template>
