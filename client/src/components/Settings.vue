<script setup lang="ts">
import { ref } from "vue";
import { settings, THEMES } from "../settings";
import { connection } from "../connection";
import { enablePush, pushSupported } from "../pwa";
import { authState, logout } from "../auth";

const emit = defineEmits<{ close: [] }>();
const pushMsg = ref("");

async function enableNotifications() {
  pushMsg.value = "requesting…";
  const ok = await enablePush();
  pushMsg.value = ok ? "Notifications enabled ✓" : "Not enabled (permission denied or unsupported)";
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

      <div v-if="connection.hasCap('push') && pushSupported()" class="row">
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
